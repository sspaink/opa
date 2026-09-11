package handlers

import (
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	acceptEncodingHeader  = "Accept-Encoding"
	contentEncodingHeader = "Content-Encoding"
	contentLengthHeader   = "Content-Length"
	gzipEncodingValue     = "gzip"
)

// This handler applies only for data and compile endpoints, for selected HTTP methods
//
// If the client asked for a gzip response, this handler will buffer the response and
// wait until it reached a certain threshold. If the threshold is not hit, the uncompressed response is sent
//
// If a gzip response is not asked by the client, it'll send the uncompressed response
//
// The threshold and the gzip compression level can be modified from server's configuration

func CompressHandler(handler http.Handler, gzipMinLength int, gzipCompressionLevel int) http.Handler {
	pool := getGzipPool(gzipCompressionLevel)

	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		enabledForEndpoint := isDataEndpoint(request) || isCompileEndpoint(request)
		if !enabledForEndpoint {
			handler.ServeHTTP(responseWriter, request)
			return
		}

		responseWriter.Header().Add("Vary", acceptEncodingHeader)

		if !gzipHeaderDetected(request.Header) {
			handler.ServeHTTP(responseWriter, request)
			return
		}

		crw := &compressResponseWriter{
			ResponseWriter: responseWriter,
			headerWritten:  false,
			minlength:      gzipMinLength,
			pool:           pool,
		}
		defer crw.Close()
		handler.ServeHTTP(crw, request)
	})
}

type compressResponseWriter struct {
	gzipWriter *gzip.Writer
	http.ResponseWriter
	pool          *gzipWriterPool
	buffer        []byte
	statusCode    int
	headerWritten bool
	minlength     int
}

// gzipWriterPool pools gzip writers built at one compression level. Handlers
// hold on to the pool they were built with and return their writers to it, so a
// configuration reload that changes the level cannot hand a writer compressing
// at the old level to the new handler.
type gzipWriterPool struct {
	pool  sync.Pool
	level int
}

var (
	gzipPoolMtx sync.Mutex
	gzipPool    atomic.Pointer[gzipWriterPool]
)

// getGzipPool returns the shared pool for the given compression level, building
// a new one if the level has changed. The pool is shared so that handlers built
// with the same level -- the common case, where nothing has been reloaded --
// reuse each other's writers.
func getGzipPool(compressionLevel int) *gzipWriterPool {
	if p := gzipPool.Load(); p != nil && p.level == compressionLevel {
		return p
	}

	gzipPoolMtx.Lock()
	defer gzipPoolMtx.Unlock()

	if p := gzipPool.Load(); p != nil && p.level == compressionLevel {
		return p
	}

	p := &gzipWriterPool{level: compressionLevel}
	p.pool.New = func() any {
		writer, _ := gzip.NewWriterLevel(io.Discard, compressionLevel)
		return writer
	}
	gzipPool.Store(p)

	return p
}

func (w *compressResponseWriter) WriteHeader(statusCode int) {
	// save the status code for later use
	w.statusCode = statusCode
}

func (w *compressResponseWriter) Write(bytes []byte) (int, error) {
	if w.isGzipInitialized() {
		return w.gzipWriter.Write(bytes)
	}

	// accumulate the buffer
	w.buffer = append(w.buffer, bytes...)

	// if the buffer is above threshold, use compression
	if len(w.buffer) >= w.minlength {
		err := w.doCompressedResponse()
		if err != nil {
			return 0, err
		}
		return len(bytes), nil
	}

	// wait for more data
	return len(bytes), nil
}

func (w *compressResponseWriter) Flush() {
	if w.isGzipInitialized() {
		w.gzipWriter.Flush()
		flusher, canFlush := w.ResponseWriter.(http.Flusher)
		if canFlush {
			flusher.Flush()
		}
	}
}

func (w *compressResponseWriter) Close() error {
	if !w.isGzipInitialized() {
		// gzip didn't handle the response, send it plain
		err := w.doUncompressedResponse()
		if err != nil {
			err = fmt.Errorf("error writing uncompressed data: %v", err.Error())
		}
		return err
	}

	err := w.gzipWriter.Close()
	if err != nil {
		return err
	}

	w.pool.pool.Put(w.gzipWriter)
	w.gzipWriter = nil

	return err
}

func (w *compressResponseWriter) doCompressedResponse() error {
	w.ResponseWriter.Header().Set(contentEncodingHeader, gzipEncodingValue)
	w.Header().Del(contentLengthHeader)
	w.writeHeader()
	// there's nothing to write
	if len(w.buffer) == 0 {
		return nil
	}
	gzipWriter := w.pool.pool.Get().(*gzip.Writer)
	gzipWriter.Reset(w.ResponseWriter)
	w.gzipWriter = gzipWriter
	_, err := w.gzipWriter.Write(w.buffer)
	return err
}

func (w *compressResponseWriter) doUncompressedResponse() error {
	w.writeHeader()
	// there's nothing to write
	if w.buffer == nil {
		return nil
	}
	_, err := w.ResponseWriter.Write(w.buffer)
	w.buffer = nil
	return err
}

func (w *compressResponseWriter) isGzipInitialized() bool {
	return w.gzipWriter != nil
}

func (w *compressResponseWriter) writeHeader() {
	if !w.headerWritten && w.statusCode != 0 {
		w.ResponseWriter.WriteHeader(w.statusCode)
		w.headerWritten = true
	}
}

func isDataEndpoint(req *http.Request) bool {
	isPostOrGetMethod := isPostMethod(req) || isGetMethod(req)
	isV1rV0 := strings.HasPrefix(req.URL.Path, "/v1/data") || strings.HasPrefix(req.URL.Path, "/v0/data")
	return isPostOrGetMethod && isV1rV0
}

func isCompileEndpoint(req *http.Request) bool {
	return isPostMethod(req) && strings.HasPrefix(req.URL.Path, "/v1/compile")
}

func isPostMethod(req *http.Request) bool {
	return req.Method == "POST"
}

func isGetMethod(req *http.Request) bool {
	return req.Method == "GET"
}

func gzipHeaderDetected(header http.Header) bool {
	for part := range strings.SplitSeq(header.Get("Accept-Encoding"), ",") {
		part = strings.TrimSpace(part)
		if part == gzipEncodingValue || strings.HasPrefix(part, gzipEncodingValue+";") {
			return true
		}
	}
	return false
}
