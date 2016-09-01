package server

import (
	"encoding/json"
	"errors"

	"github.com/porthos-rpc/porthos-go/log"
	"github.com/streadway/amqp"
)

// Response represents a rpc response.
type Response struct {
	content     []byte
	contentType string
	statusCode  int16
}

// ResponseWriter is responsible for sending back the response to the replyTo queue.
type ResponseWriter struct {
	channel  *amqp.Channel
	autoAck  bool
	delivery amqp.Delivery
}

// JSON sets the content of the response as JSON data.
func (r *Response) JSON(statusCode int16, content interface{}) {
	if content == nil {
		panic("Response content is empty")
	}

	jsonContent, err := json.Marshal(&content)

	if err != nil {
		panic(err)
	}

	r.statusCode = statusCode
	r.content = jsonContent
	r.contentType = "application/json"
}

// Raw sets the content of the response as an array of bytes.
func (r *Response) Raw(statusCode int16, contentType string, content []byte) {
	if content == nil {
		panic("Response content is empty")
	}

	r.statusCode = statusCode
	r.content = content
	r.contentType = contentType
}

// Empty leaves the content of the response as empty.
func (r *Response) Empty(statusCode int16) {
	r.statusCode = statusCode
}

// Write the response back to the ReplyTo queue.
func (rw *ResponseWriter) Write(res *Response) error {
	log.Info("Sending response to queue '%s'. Slot: '%d'", rw.delivery.ReplyTo, []byte(rw.delivery.CorrelationId))

	if rw.channel == nil {
		return errors.New("No AMQP channel to publish the response to.")
	}

	err := rw.channel.Publish(
		"",
		rw.delivery.ReplyTo,
		false,
		false,
		amqp.Publishing{
			Headers: amqp.Table{
				"statusCode": res.statusCode,
			},
			ContentType:   res.contentType,
			CorrelationId: rw.delivery.CorrelationId,
			Body:          res.content,
		})

	if err != nil {
		return err
	}

	if !rw.autoAck {
		rw.delivery.Ack(false)
		log.Debug("Ack from slot '%d'", []byte(rw.delivery.CorrelationId))
	}

	return nil
}
