package porthos

import (
	"sync"
	"time"

	"github.com/porthos-rpc/porthos-go/log"
	"github.com/streadway/amqp"
)

// MethodHandler represents a rpc method handler.
type MethodHandler func(req Request, res Response)

// Server is used to register procedures to be invoked remotely.
type Server interface {
	// Register a method and its handler.
	Register(method string, handler MethodHandler)
	// Register a method and its handler.
	RegisterWithSpec(method string, handler MethodHandler, spec Spec)
	// AddExtension adds extensions to the server instance.
	// Extensions can be used to add custom actions to incoming and outgoing RPC calls.
	AddExtension(ext Extension)
	// ListenAndServe start serving RPC requests.
	ListenAndServe()
	// GetServiceName returns the name of this service.
	GetServiceName() string
	// GetSpecs returns all registered specs.
	GetSpecs() map[string]Spec
	// Close the client and AMQP channel.
	// This method returns right after the AMQP channel is closed.
	// In order to give time to the current request to finish (if there's one)
	// it's up to you to wait using the NotifyClose.
	Close()
	// Shutdown shuts down the client and AMQP channel.
	// It provider graceful shutdown, since it will wait the result
	// of <-s.NotifyClose().
	Shutdown()
	// NotifyClose returns a channel to be notified then this server closes.
	NotifyClose() <-chan bool
}

type server struct {
	m              sync.Mutex
	broker         *Broker
	serviceName    string
	channel        *amqp.Channel
	requestChannel <-chan amqp.Delivery
	methods        map[string]MethodHandler
	specs          map[string]Spec
	autoAck        bool
	extensions     []Extension
	topologySet    bool

	closed bool
	closes []chan bool
}

// Options represent all the options supported by the server.
type Options struct {
	AutoAck bool
}

var servePollInterval = 500 * time.Millisecond

// NewServer creates a new instance of Server, responsible for executing remote calls.
func NewServer(b *Broker, serviceName string, options Options) (Server, error) {
	s := &server{
		broker:      b,
		serviceName: serviceName,
		methods:     make(map[string]MethodHandler),
		specs:       make(map[string]Spec),
		autoAck:     options.AutoAck,
	}

	err := s.setupTopology()

	if err != nil {
		return nil, err
	}

	go s.handleReestablishedConnnection()

	return s, nil
}

func (s *server) setupTopology() error {
	s.m.Lock()
	defer s.m.Unlock()

	var err error
	s.channel, err = s.broker.openChannel()

	if err != nil {
		return err
	}

	// create the response queue (let the amqp server to pick a name for us)
	_, err = s.channel.QueueDeclare(
		s.serviceName, // name
		true,          // durable
		false,         // delete when usused
		false,         // exclusive
		false,         // noWait
		nil,           // arguments
	)

	if err != nil {
		s.channel.Close()
		return err
	}

	s.requestChannel, err = s.channel.Consume(
		s.serviceName, // queue
		"",            // consumer
		s.autoAck,     // auto-ack
		false,         // exclusive
		false,         // no-local
		false,         // no-wait
		nil,           // args
	)

	if err != nil {
		s.channel.Close()
		return err
	}

	s.topologySet = true

	return nil
}

func (s *server) handleReestablishedConnnection() {
	notifyCh := s.broker.NotifyReestablish()

	for !s.closed {
		<-notifyCh

		err := s.setupTopology()

		if err != nil {
			log.Error("Error setting up topology after reconnection [%s]", err)
		}
	}
}

func (s *server) serve() {
	for !s.closed {
		if s.topologySet {
			s.pipeThroughServerListeningExtensions()
			s.printRegisteredMethods()

			log.Info("Connected to the broker and waiting for incoming rpc requests...")

			for d := range s.requestChannel {
				go s.processRequest(d)
			}

			s.topologySet = false
		} else {
			time.Sleep(servePollInterval)
		}
	}

	for _, c := range s.closes {
		c <- true
	}
}

func (s *server) printRegisteredMethods() {
	log.Info("[%s]", s.serviceName)

	for method := range s.methods {
		log.Info(". %s", method)
	}
}

func (s *server) processRequest(d amqp.Delivery) {
	methodName := d.Headers["X-Method"].(string)

	if method, ok := s.methods[methodName]; ok {
		req := &request{s.serviceName, methodName, d.ContentType, d.Body}
		ch, err := s.broker.openChannel()

		if err != nil {
			log.Error("Error opening channel for response: '%s'", err)
		}

		defer ch.Close()

		resWriter := &responseWriter{delivery: d, channel: ch, autoAck: s.autoAck}

		res := newResponse()
		method(req, res)

		err = resWriter.Write(res)

		if err != nil {
			log.Error("Error writing response: '%s'", err.Error())
		}
	} else {
		log.Error("Method '%s' not found.", methodName)
		if !s.autoAck {
			d.Reject(false)
		}
	}
}

func (s *server) pipeThroughServerListeningExtensions() {
	for _, ext := range s.extensions {
		ext.ServerListening(s)
	}
}

func (s *server) pipeThroughIncomingExtensions(req Request) {
	for _, ext := range s.extensions {
		ext.IncomingRequest(req)
	}
}

func (s *server) pipeThroughOutgoingExtensions(req Request, res Response, responseTime time.Duration) {
	for _, ext := range s.extensions {
		ext.OutgoingResponse(req, res, responseTime, res.GetStatusCode())
	}
}

func (s *server) Register(method string, handler MethodHandler) {
	s.methods[method] = func(req Request, res Response) {
		s.pipeThroughIncomingExtensions(req)

		started := time.Now()

		// invoke the registered function.
		handler(req, res)

		s.pipeThroughOutgoingExtensions(req, res, time.Since(started))
	}
}

func (s *server) RegisterWithSpec(method string, handler MethodHandler, spec Spec) {
	s.Register(method, handler)
	s.specs[method] = spec
}

// GetServiceName returns the name of this service.
func (s *server) GetServiceName() string {
	return s.serviceName
}

// GetSpecs returns all registered specs.
func (s *server) GetSpecs() map[string]Spec {
	return s.specs
}

func (s *server) AddExtension(ext Extension) {
	s.extensions = append(s.extensions, ext)
}

func (s *server) ListenAndServe() {
	s.serve()
}

func (s *server) Close() {
	s.closed = true
	s.channel.Close()
}

func (s *server) Shutdown() {
	ch := make(chan bool)

	go func() {
		ch <- <-s.NotifyClose()
	}()

	s.Close()
	<-ch
}

func (s *server) NotifyClose() <-chan bool {
	receiver := make(chan bool)
	s.closes = append(s.closes, receiver)

	return receiver
}
