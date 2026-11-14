package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// recorder is a backend that remembers every request body it received.
type recorder struct {
	bodies []map[string]any
}

func (r *recorder) backend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("backend got a body that is not JSON: %v", err)
		}
		r.bodies = append(r.bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reply":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (r *recorder) last(t *testing.T) map[string]any {
	t.Helper()
	if len(r.bodies) == 0 {
		t.Fatal("the backend was never called")
	}
	return r.bodies[len(r.bodies)-1]
}

// The greeting travels with the first turn so the backend knows what the
// client already saw.
func TestSendCarriesInitialAssistantMessage(t *testing.T) {
	rec := &recorder{}
	srv := rec.backend(t)

	client, err := New(Spec{Name: "concierge", Transport: srv.URL, Sessions: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Send(context.Background(), Call{
		Message:                 "hello",
		SessionID:               "s-1",
		InitialAssistantMessage: "Hello there",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	body := rec.last(t)
	got, ok := body["initial_assistant_message"].(string)
	if !ok {
		t.Fatalf("body has no initial_assistant_message: %v", body)
	}
	if got != "Hello there" {
		t.Errorf("initial_assistant_message = %q, want %q", got, "Hello there")
	}
}

// Without a greeting the field is absent, not empty: later turns carry no
// trace of it.
func TestSendOmitsInitialAssistantMessageWhenEmpty(t *testing.T) {
	rec := &recorder{}
	srv := rec.backend(t)

	client, err := New(Spec{Name: "concierge", Transport: srv.URL, Sessions: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.Send(context.Background(), Call{Message: "hello", SessionID: "s-1"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	body := rec.last(t)
	if v, present := body["initial_assistant_message"]; present {
		t.Errorf("body carries initial_assistant_message=%v, want it absent", v)
	}
}

// The greeting is configuration: the client exposes it without asking anyone.
func TestStartMessageComesFromTheSpec(t *testing.T) {
	client, err := New(Spec{
		Name:         "concierge",
		Transport:    "http://127.0.0.1:1/vireo/message",
		StartMessage: "Hello there",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := client.StartMessage(); got != "Hello there" {
		t.Errorf("StartMessage() = %q, want %q", got, "Hello there")
	}
}

// An agent without a greeting says nothing when a conversation starts.
func TestStartMessageIsEmptyWhenNotConfigured(t *testing.T) {
	client, err := New(Spec{Name: "orders", Transport: "http://127.0.0.1:1/vireo/message"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := client.StartMessage(); got != "" {
		t.Errorf("StartMessage() = %q, want it empty", got)
	}
}
