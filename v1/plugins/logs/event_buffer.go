package logs

import (
	"bytes"
	"compress/gzip"
	"context"

	"github.com/open-policy-agent/opa/v1/plugins/rest"
)

// EventBuffer stores and uploads JSON encoded EventV1 data.
// The oldest events will be dropped if the buffer is full.
type EventBuffer struct {
	buffer chan []byte

	Stop   chan chan struct{}
	Errors chan error
}

func NewEventBuffer(limit int64) *EventBuffer {
	return &EventBuffer{
		buffer: make(chan []byte, limit),
		Stop:   make(chan chan struct{}),
		Errors: make(chan error, 1),
	}
}

// Push adds a new event to the buffer, if full drops the oldest
func (e *EventBuffer) Push(event []byte) {
	push(e.buffer, event)
}

// pushError holds the most recent error, used to set the log plugins Status
func (e *EventBuffer) pushError(err error) {
	push(e.Errors, err)
}

func push[T any](ch chan T, data T) {
	select {
	case ch <- data:
	default:
		<-ch
		ch <- data
	}
}

// Upload reads events from the buffer to create a gzip compressed JSON array of events.
// Events will be read until either the upload size limit is reached or a stop signal is sent.
// Once a stopping condition is met, the JSON events array will be uploaded to the configured service.
func (e *EventBuffer) Upload(ctx context.Context, client rest.Client, uploadSizeLimitBytes int64, uploadPath string) {
	var bytesWritten int
	uploadBuffer := new(bytes.Buffer)
	w := gzip.NewWriter(uploadBuffer)

	for {
		select {
		case event := <-e.buffer:
			if len(event) == 0 {
				continue
			}

			// Upload size limit reached, close the JSON array and upload, requeue new event.
			// The +1 is for the final closing bracket.
			if int64(len(event)+bytesWritten+1) > uploadSizeLimitBytes {
				if err := upload(ctx, w, client, uploadPath, uploadBuffer); err != nil {
					e.pushError(err)
					return
				}

				// Requeue the event that exceeded the upload size limit
				e.Push(event)
				return
			}

			switch bytesWritten {
			case 0: // Start new JSON array
				n, err := w.Write([]byte(`[`))
				if err != nil {
					e.pushError(err)
					return
				}
				bytesWritten += n
			default: // Append new event to JSON array
				n, err := w.Write([]byte(`,`))
				if err != nil {
					e.pushError(err)
					return
				}
				bytesWritten += n
			}

			n, err := w.Write(event)
			if err != nil {
				e.pushError(err)
				return
			}
			bytesWritten += n

		case done := <-e.Stop:
			if bytesWritten != 0 {
				if err := upload(ctx, w, client, uploadPath, uploadBuffer); err != nil {
					e.pushError(err)
					done <- struct{}{}
					return
				}
			}

			done <- struct{}{}
			return
		}
	}
}

// upload closes the JSON array and attempts to send the data to the configured service
func upload(ctx context.Context, w *gzip.Writer, client rest.Client, uploadPath string, data *bytes.Buffer) error {
	_, err := w.Write([]byte(`]`))
	if err != nil {
		return err
	}

	if err := w.Close(); err != nil {
		return err
	}

	if err := uploadChunk(ctx, client, uploadPath, data.Bytes()); err != nil {
		return err
	}

	return nil
}
