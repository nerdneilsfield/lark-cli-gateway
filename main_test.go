package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func validMessage() Message {
	return Message{ChatID: "oc_test", As: "bot", Type: "markdown", Content: "hello"}
}

func TestHandleSendValidQueues(t *testing.T) {
	queue := make(chan Message, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", handleSend(queue))

	body, err := json.Marshal(validMessage())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	if rec.Body.String() != `{"ok":true}` {
		t.Fatalf("body = %q, want %q", rec.Body.String(), `{"ok":true}`)
	}
	if got := <-queue; got != validMessage() {
		t.Fatalf("queued = %+v, want %+v", got, validMessage())
	}
}

func TestHandleSendRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"chat_id":`},
		{"missing chat_id", `{"as":"bot","type":"text","content":"hi"}`},
		{"missing as", `{"chat_id":"oc_test","type":"text","content":"hi"}`},
		{"missing type", `{"chat_id":"oc_test","as":"bot","content":"hi"}`},
		{"missing content", `{"chat_id":"oc_test","as":"bot","type":"text"}`},
		{"bad as", `{"chat_id":"oc_test","as":"admin","type":"text","content":"hi"}`},
		{"bad type", `{"chat_id":"oc_test","as":"bot","type":"html","content":"hi"}`},
		{"two JSON values", `{"chat_id":"oc_test","as":"bot","type":"text","content":"hi"}{"x":1}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queue := make(chan Message, 1)
			mux := http.NewServeMux()
			mux.HandleFunc("POST /send", handleSend(queue))

			req := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if len(queue) != 0 {
				t.Fatalf("queue len = %d, want 0", len(queue))
			}
		})
	}
}

func TestHandleSendQueueFullReturns503(t *testing.T) {
	queue := make(chan Message, 1)
	queue <- validMessage()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", handleSend(queue))

	body, err := json.Marshal(validMessage())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/send", strings.NewReader(string(body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if len(queue) != 1 {
		t.Fatalf("queue len = %d, want 1", len(queue))
	}
}

func TestSendWithRetryZeroRetriesCallsOnce(t *testing.T) {
	calls := 0
	sendWithRetry(validMessage(), 0, 0, func(Message) error {
		calls++
		return nil
	})
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestSendWithRetryStopsAfterSuccess(t *testing.T) {
	calls := 0
	sendWithRetry(validMessage(), 2, 0, func(Message) error {
		calls++
		if calls < 3 {
			return errors.New("boom")
		}
		return nil
	})
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestSendWithRetryPermanentFailureCallsThreeTimes(t *testing.T) {
	calls := 0
	sendWithRetry(validMessage(), 2, 0, func(Message) error {
		calls++
		return errors.New("boom")
	})
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestWorkerProcessesInFIFOOrder(t *testing.T) {
	queue := make(chan Message, 2)
	queue <- Message{ChatID: "first", As: "bot", Type: "text", Content: "1"}
	queue <- Message{ChatID: "second", As: "bot", Type: "text", Content: "2"}
	close(queue)

	var calls []string
	worker(queue, 0, 0, 2, func(m Message) error {
		calls = append(calls, m.ChatID)
		if m.ChatID == "first" {
			return errors.New("boom")
		}
		return nil
	})

	if len(calls) != 4 {
		t.Fatalf("calls = %v, want 4 calls", calls)
	}
	// The second message may only be attempted after the first exhausted
	// its retries: first, first, first, second.
	if calls[0] != "first" || calls[1] != "first" || calls[2] != "first" || calls[3] != "second" {
		t.Fatalf("order = %v, want [first first first second]", calls)
	}
}
