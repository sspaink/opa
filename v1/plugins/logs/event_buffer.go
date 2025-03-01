package logs

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"

	"github.com/open-policy-agent/opa/v1/plugins/rest"
)

// eventBuffer stores and uploads gzipped JSON encoded EventV1 data
type eventBuffer struct {
	Stop  chan chan struct{} // Stop is used to end the upload early, flushing the current buffer state
	Error chan error         // Error contains the latest error thrown during the upload loop

	buffer               chan []byte   // buffer is a buffered channel storing the JSON encoded EventV1 data
	client               rest.Client   // client is used to upload the data to the configured service
	uploadPath           string        // uploadPath is the user configured HTTP resource path the client will upload to
	uploadSizeLimitBytes int64         // uploadSizeLimitBytes will enforce a maximum payload size to be uploaded
	uploadBuffer         *bytes.Buffer // uploadBuffer is the temporary buffer to construct the JSON array used in between uploads
	writer               *gzip.Writer  // writer writes to the uploadBuffer
	bytesWritten         int           // bytesWritten is used to enforce the limit defined by uploadSizeLimitBytes
}

func newEventBuffer(limit int64) *eventBuffer {
	return &eventBuffer{
		buffer: make(chan []byte, limit),
		Stop:   make(chan chan struct{}),
		Error:  make(chan error, 1),
	}
}

type droppedNDCacheError struct {
}

func (d droppedNDCacheError) Error() string {
	return "ND builtins cache dropped from this event to fit under maximum upload size limits. Increase upload size limit or change usage of non-deterministic builtins."
}

// Push adds a new event to the buffer.
// Reasons for an event to be dropped:
// * Invalid JSON
// * If the event is bigger than the limit (attempts to reduce the size by removing the non-deterministic cache)
// * If the buffer is full drops the oldest event is dropped
func (e *eventBuffer) Push(event EventV1, uploadSizeLimitBytes int64) error {
	var err error
	result, err := json.Marshal(event)
	if err != nil {
		return err
	}

	// If event is bigger than UploadSizeLimitBytes, try to drop the NDBuiltinCache.
	// If the event is still too big, drop the event.
	if int64(len(result)) > uploadSizeLimitBytes {
		if event.NDBuiltinCache == nil {
			return fmt.Errorf("upload chunk size (%d) exceeds upload_size_limit_bytes (%d)", int64(len(result)), uploadSizeLimitBytes)
		}
		event.NDBuiltinCache = nil
		result, err = json.Marshal(event)
		if err != nil {
			return err
		}
		if int64(len(result)) > uploadSizeLimitBytes {
			return fmt.Errorf("upload chunk size (%d) exceeds upload_size_limit_bytes (%d)", int64(len(result)), uploadSizeLimitBytes)
		}

		err = droppedNDCacheError{}
	}

	push(e.buffer, result)
	return err
}

// pushError holds the most recent error, used to set the log plugins Status
func (e *eventBuffer) pushError(err error) {
	push(e.Error, err)
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
// Once a stopping condition is met, the JSON events array (stored in uploadBuffer) will be uploaded.
// When stopped using the Stop channel, the current content in the buffer is flushed and uploaded.
// Upload should run in a goroutine and be closed with the Stop channel before starting another.
func (e *eventBuffer) Upload(ctx context.Context, client rest.Client, uploadSizeLimitBytes int64, uploadPath string) {
	e.client = client
	e.uploadPath = uploadPath
	e.uploadSizeLimitBytes = uploadSizeLimitBytes

	// new upload buffer, reset after each upload
	e.bytesWritten = 0
	e.uploadBuffer = new(bytes.Buffer)
	e.writer = gzip.NewWriter(e.uploadBuffer)

	for {
		select {
		case event := <-e.buffer:
			err := e.processEvent(ctx, event)
			if err != nil {
				e.pushError(err)
				return
			}
		case done := <-e.Stop:
			// Flush the current events stored in the buffer.
			// Prevent events being stuck in the buffer due to aggressive upload requests.
			if err := e.flush(ctx, len(e.buffer)); err != nil {
				e.pushError(err)
			}

			if e.bytesWritten != 0 {
				if err := e.upload(ctx); err != nil {
					e.pushError(err)
				}
			}

			done <- struct{}{}
			return
		}
	}
}

// flush will attempt to upload the requested amount of events from the buffer
func (e *eventBuffer) flush(ctx context.Context, len int) error {
	for range len {
		select {
		case event := <-e.buffer:
			err := e.processEvent(ctx, event)
			if err != nil {
				return err
			}
		default:
			return nil
		}
	}

	return nil
}

func (e *eventBuffer) processEvent(ctx context.Context, event []byte) error {
	if len(event) == 0 {
		return nil
	}

	// Upload size limit reached, close the JSON array and upload, requeue new event.
	// The +1 is for the final closing bracket.
	if int64(len(event)+e.bytesWritten+1) > e.uploadSizeLimitBytes {
		if err := e.upload(ctx); err != nil {
			return err
		}
		// Requeue the event that exceeded the upload size limit
		push(e.buffer, event)
		return nil
	}

	switch e.bytesWritten {
	case 0: // Start new JSON array
		n, err := e.writer.Write([]byte(`[`))
		if err != nil {
			return err
		}
		e.bytesWritten += n
	default: // Append new event to JSON array
		n, err := e.writer.Write([]byte(`,`))
		if err != nil {
			return err
		}
		e.bytesWritten += n
	}

	n, err := e.writer.Write(event)
	if err != nil {
		return err
	}

	e.bytesWritten += n

	return nil
}

// upload closes the JSON array and attempts to send the data to the configured service
func (e *eventBuffer) upload(ctx context.Context) error {
	_, err := e.writer.Write([]byte(`]`))
	if err != nil {
		return err
	}

	if err := e.writer.Close(); err != nil {
		return err
	}

	if err := uploadChunk(ctx, e.client, e.uploadPath, e.uploadBuffer.Bytes()); err != nil {
		return err
	}

	// reset upload buffer
	e.bytesWritten = 0
	e.uploadBuffer = new(bytes.Buffer)
	e.writer = gzip.NewWriter(e.uploadBuffer)

	return nil
}
