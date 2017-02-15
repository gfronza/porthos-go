package errors

import (
	"errors"
)

var (
	ErrTimedOut          = errors.New("timed out")
	ErrNilPublishChannel = errors.New("No AMQP channel to publish the response to.")
)