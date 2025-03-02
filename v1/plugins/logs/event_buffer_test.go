package logs

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/open-policy-agent/opa/v1/ast"
	"github.com/open-policy-agent/opa/v1/keys"
	"github.com/open-policy-agent/opa/v1/plugins/rest"
	"github.com/open-policy-agent/opa/v1/topdown/builtins"
)

func TestEventBuffer_Push(t *testing.T) {
	limit := int64(2)
	e := newEventBuffer(limit)
	err := e.Push(newTestEvent(t, "1", false), 500)
	if err != nil {
		t.Fatal(err)
	}
	err = e.Push(newTestEvent(t, "2", false), 500)
	if err != nil {
		t.Fatal(err)
	}
	err = e.Push(newTestEvent(t, "3", false), 500)
	if err != nil {
		t.Fatal(err)
	}

	err = e.Push(newTestEvent(t, "4", false), 100)
	expectedErrorMsg := "upload chunk size (195) exceeds upload_size_limit_bytes (100)"
	if err == nil {
		t.Fatal("error expected")
	} else if err.Error() != expectedErrorMsg {
		t.Fatalf("expected error %v but got %v", expectedErrorMsg, err.Error())
	}

	err = e.Push(newTestEvent(t, "5", true), 195)
	expectedErrorMsg = droppedNDCache{}.Error()
	if err == nil {
		t.Fatal("error expected")
	} else if err.Error() != expectedErrorMsg {
		t.Fatalf("expected error %v but got %v", expectedErrorMsg, err.Error())
	}

	err = e.Push(newTestEvent(t, "6", true), 194)
	expectedErrorMsg = "upload chunk size (195) exceeds upload_size_limit_bytes (194)"
	if err == nil {
		t.Fatal("error expected")
	} else if err.Error() != expectedErrorMsg {
		t.Fatalf("expected error %v but got %v", expectedErrorMsg, err.Error())
	}

	close(e.buffer)

	events := make([]EventV1, 0, 2)
	for event := range e.buffer {
		var e EventV1
		if err := json.Unmarshal(event, &e); err != nil {
			t.Fatal(err)
		}
		if e.DecisionID == "1" {
			t.Fatal("got unexpected decision ID 1")
		}

		events = append(events, e)
	}

	if int64(len(events)) != limit {
		t.Errorf("EventBuffer pushed %d events, expected %d", len(events), limit)
	}
}

func TestEventBuffer_Upload(t *testing.T) {
	uploadPath := "/v1/test"

	tests := []struct {
		name                 string
		eventLimit           int64
		numberOfEvents       int
		uploadSizeLimitBytes int64
		handleFunc           func(w http.ResponseWriter, r *http.Request)
		postUploadFunc       func(e *eventBuffer)
		expectedError        string
	}{
		{
			name:                 "Trigger upload with stop channel",
			eventLimit:           4,
			numberOfEvents:       3,
			uploadSizeLimitBytes: defaultUploadSizeLimitBytes,
			handleFunc: func(w http.ResponseWriter, r *http.Request) {
				events := readEventBody(t, r.Body)
				if len(events) != 3 {
					t.Errorf("expected 3 events, got %d", len(events))
				}

				w.WriteHeader(http.StatusOK)
			},
			postUploadFunc: func(e *eventBuffer) {
				for len(e.buffer) != 0 {
					time.Sleep(1 * time.Second)
				}

				done := make(chan struct{})
				e.Stop <- done
				<-done
			},
		},
		{
			name:                 "Trigger upload due to hitting upload size limit",
			eventLimit:           4,
			numberOfEvents:       4,
			uploadSizeLimitBytes: 400, // Each test event is 195 bytes
			handleFunc: func(w http.ResponseWriter, r *http.Request) {
				events := readEventBody(t, r.Body)
				if len(events) != 2 {
					t.Errorf("expected 2 events, got %d", len(events))
				}
				if events[0].DecisionID != "0" {
					t.Errorf("expected 1 decision ID, got %s", events[0].DecisionID)
				}
				if events[1].DecisionID != "1" {
					t.Errorf("expected 2 decision ID, got %s", events[1].DecisionID)
				}
				w.WriteHeader(http.StatusOK)
			},
			postUploadFunc: func(e *eventBuffer) {
				for len(e.buffer) != 0 {
					time.Sleep(1 * time.Second)
				}
			},
		},
		{
			name:                 "Get error from failed upload",
			eventLimit:           1,
			numberOfEvents:       1,
			uploadSizeLimitBytes: defaultUploadSizeLimitBytes,
			handleFunc: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
			},
			postUploadFunc: func(e *eventBuffer) {
				// Wait until buffer is empty
				for len(e.buffer) != 0 {
					time.Sleep(1 * time.Second)
				}

				done := make(chan struct{})
				e.Stop <- done
				<-done
			},
			expectedError: "log upload failed, server replied with HTTP 400 Bad Request",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := newEventBuffer(tc.eventLimit)

			for i := range tc.numberOfEvents {
				err := e.Push(newTestEvent(t, strconv.Itoa(i), false), tc.uploadSizeLimitBytes)
				if err != nil {
					t.Fatal(err)
				}
			}

			client, ts := setupTestServer(t, uploadPath, tc.handleFunc)
			defer ts.Close()

			go e.Upload(context.Background(), client, tc.uploadSizeLimitBytes, uploadPath)

			tc.postUploadFunc(e)

			select {
			case err := <-e.Error:
				if tc.expectedError != "" && err.Error() != tc.expectedError {
					t.Fatal(err)
				}
			default:
				if tc.expectedError != "" {
					t.Fatalf("The following error was expected: %s", tc.expectedError)
				}
			}
		})
	}
}

func newTestEvent(t *testing.T, id string, enableNDCache bool) EventV1 {
	var result interface{} = false
	var expInput interface{} = map[string]interface{}{"method": "GET"}
	timestamp, err := time.Parse(time.RFC3339Nano, "2018-01-01T12:00:00.123456Z")
	if err != nil {
		t.Fatal(err)
	}
	e := EventV1{
		Labels: map[string]string{
			"id":  "test-instance-id",
			"app": "example-app",
		},
		DecisionID:  id,
		Path:        "foo/bar",
		Input:       &expInput,
		Result:      &result,
		RequestedBy: "test",
		Timestamp:   timestamp,
	}

	if enableNDCache {
		var ndbCacheExample = ast.MustJSON(builtins.NDBCache{
			"time.now_ns": ast.NewObject([2]*ast.Term{
				ast.ArrayTerm(),
				ast.NumberTerm("1663803565571081429"),
			}),
		}.AsValue())
		e.NDBuiltinCache = &ndbCacheExample
	}

	return e
}

func setupTestServer(t *testing.T, uploadPath string, handleFunc func(w http.ResponseWriter, r *http.Request)) (rest.Client, *httptest.Server) {
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)

	mux.HandleFunc(uploadPath, handleFunc)

	config := fmt.Sprintf(`{
		"name": "foo",
		"url": %q,
		"response_header_timeout_seconds": 20,
	}`, ts.URL)
	ks := map[string]*keys.Config{}
	client, err := rest.New([]byte(config), ks)
	if err != nil {
		t.Fatal(err)
	}

	return client, ts
}

func readEventBody(t *testing.T, r io.Reader) []EventV1 {
	gr, err := gzip.NewReader(r)
	if err != nil {
		t.Fatal(err)
	}
	var events []EventV1
	if err := json.NewDecoder(gr).Decode(&events); err != nil {
		t.Fatal(err)
	}
	if err := gr.Close(); err != nil {
		t.Fatal(err)
	}

	return events
}
