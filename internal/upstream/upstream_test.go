package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"vireo/internal/protocol"
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

// A backend that answers with newline-delimited JSON can say "hold on" before
// the reply; each partial line reaches OnPartial in order, and the final line
// is the reply.
func TestSendStreamsPartialLines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); !strings.Contains(got, "application/x-ndjson") {
			t.Errorf("Accept = %q, want it to offer ndjson", got)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, `{"partial":"fetching your site…"}`+"\n")
		_, _ = io.WriteString(w, `{"partial":"building the assistant…"}`+"\n")
		_, _ = io.WriteString(w, `{"reply":"done","session":{"summary":"created"}}`+"\n")
	}))
	t.Cleanup(srv.Close)

	client, err := New(Spec{Name: "creator", Transport: srv.URL, Sessions: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var got []string
	resp, err := client.Send(context.Background(), Call{
		Message:   "build me one",
		SessionID: "abc",
		OnPartial: func(p protocol.PartialPayload) {
			if p.Kind != "status" {
				t.Errorf("kind = %q, want status", p.Kind)
			}
			got = append(got, p.Text)
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	want := []string{"fetching your site…", "building the assistant…"}
	if len(got) != len(want) {
		t.Fatalf("partials = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("partial[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if resp.Reply != "done" {
		t.Errorf("reply = %q, want %q", resp.Reply, "done")
	}
	if resp.Session == nil || resp.Session.Summary != "created" {
		t.Errorf("session = %+v, want summary %q", resp.Session, "created")
	}
}

// A plain JSON backend is unaffected: no partials, same reply.
func TestSendStillAcceptsWholeBodyJSON(t *testing.T) {
	rec := &recorder{}
	srv := rec.backend(t)
	client, err := New(Spec{Name: "orders", Transport: srv.URL, Sessions: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	called := false
	resp, err := client.Send(context.Background(), Call{Message: "hi", OnPartial: func(protocol.PartialPayload) { called = true }})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if called {
		t.Error("OnPartial was called for a non-streaming backend")
	}
	if resp.Reply != "ok" {
		t.Errorf("reply = %q, want %q", resp.Reply, "ok")
	}
}
