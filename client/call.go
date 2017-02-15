package client

import (
	"encoding/json"
	"strconv"
	"time"

	"github.com/porthos-rpc/porthos-go/errors"
	"github.com/porthos-rpc/porthos-go/log"
	"github.com/streadway/amqp"
)

type call struct {
	client      *Client
	timeout     time.Duration
	method      string
	body        []byte
	contentType string
}

// Map is an abstraction for map[string]interface{} to be used with WithMap.
type Map map[string]interface{}

// NewCall creates a new RPC call object.
func newCall(client *Client, method string) *call {
	return &call{client: client, method: method}
}

// WithTimeout defines the timeut for this specific call.
func (c *call) WithTimeout(timeout time.Duration) *call {
	c.timeout = timeout
	return c
}

// WithBody defines the given bytes array as the request body.
func (c *call) WithBody(body []byte) *call {
	c.body = body
	c.contentType = "application/octet-stream"

	return c
}

// WitArgs defines the given args as the request body.
func (c *call) WithArgs(args ...interface{}) *call {
	c.body, _ = json.Marshal(args)
	c.contentType = "application/porthos-args"

	return c
}

// WithMap defines the given map as the request body.
func (c *call) WithMap(body map[string]interface{}) *call {
	c.body, _ = json.Marshal(body)
	c.contentType = "application/porthos-map"

	return c
}

// Async calls the remote method with the given arguments.
// It returns a *Slot (which contains the response channel) and any possible error.
func (c *call) Async() (*Slot, error) {
	res := c.client.makeNewSlot()
	correlationID := res.getCorrelationID()

	err := c.client.channel.Publish(
		"",                   // exchange
		c.client.serviceName, // routing key
		false,                // mandatory
		false,                // immediate
		amqp.Publishing{
			Headers: amqp.Table{
				"X-Method": c.method,
			},
			Expiration:    strconv.FormatInt(c.getTimeoutInt64(), 10),
			ContentType:   c.contentType,
			CorrelationId: correlationID,
			ReplyTo:       c.client.responseQueue.Name,
			Body:          c.body,
		})

	log.Info("Published method '%s' in '%s'. Expecting response in queue '%s' and slot '%d'", c.method, c.client.serviceName, c.client.responseQueue.Name, []byte(correlationID))

	if err != nil {
		return nil, err
	}

	return res, nil
}

// Sync calls the remote method with the given arguments.
// It returns a Response and any possible error.
func (c *call) Sync() (*Response, error) {
	slot, err := c.Async()

	if err != nil {
		return nil, err
	}

	defer slot.Dispose()

	select {
	case response := <-slot.ResponseChannel():
		return &response, nil
	case <-time.After(c.getTimeout()):
		return nil, errors.ErrTimedOut
	}
}

// Void calls a remote service procedure/service which will not provide any return value.
func (c *call) Void() error {
	err := c.client.channel.Publish(
		"",                   // exchange
		c.client.serviceName, // routing key
		false,                // mandatory
		false,                // immediate
		amqp.Publishing{
			Headers: amqp.Table{
				"X-Method": c.method,
			},
			ContentType: c.contentType,
			Body:        c.body,
		})

	if err != nil {
		return err
	}

	return nil
}

func (c *call) getTimeout() time.Duration {
	if c.timeout > 0 {
		return c.timeout
	}

	return c.client.defaultTTL
}

func (c *call) getTimeoutInt64() int64 {
	t := c.client.defaultTTL

	if c.timeout > 0 {
		t = c.timeout
	}

	return int64(t / time.Millisecond)
}