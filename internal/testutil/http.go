package testutil

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
)

type RoundTripHandler struct {
	Handler http.Handler
}

func (rt *RoundTripHandler) RoundTrip(req *http.Request) (*http.Response, error) {
	rec := httptest.NewRecorder()
	rt.Handler.ServeHTTP(rec, req)
	res := rec.Result()
	res.Request = req
	return res, nil
}

func NewInProcessClient(handler http.Handler) *http.Client {
	return &http.Client{Transport: &RoundTripHandler{Handler: handler}}
}

type StreamRecorder struct {
	HeaderMap http.Header
	Code      int
	Body      io.ReadCloser
	writer    io.WriteCloser
}

func NewStreamRecorder() *StreamRecorder {
	r, w := io.Pipe()
	return &StreamRecorder{
		HeaderMap: make(http.Header),
		Code:      http.StatusOK,
		Body:      r,
		writer:    w,
	}
}

func (sr *StreamRecorder) Header() http.Header {
	return sr.HeaderMap
}

func (sr *StreamRecorder) WriteHeader(statusCode int) {
	sr.Code = statusCode
}

func (sr *StreamRecorder) Write(p []byte) (int, error) {
	return sr.writer.Write(p)
}

func (sr *StreamRecorder) Flush() {}

func (sr *StreamRecorder) Close() error {
	return sr.writer.Close()
}

func ReadAll(resp *http.Response) ([]byte, error) {
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func NewRequest(method, path string, body []byte) *http.Request {
	if body == nil {
		body = []byte{}
	}
	req := httptest.NewRequest(method, "http://in-process"+path, bytes.NewReader(body))
	return req
}
