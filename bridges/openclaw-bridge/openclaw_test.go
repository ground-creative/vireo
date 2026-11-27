package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testLogger discards output so a passing run stays readable.
func testLogger(t *testing.T) *log.Logger {
	t.Helper()
	return log.New(io.Discard, "", 0)
}

// TestSessionKeyIsStable pins the mapping from a vireo session id to the
// gateway handle. Changing it silently would orphan every existing
// conversation: the gateway would see a new user and start from nothing.
func TestSessionKeyIsStable(t *testing.T) {
	got := SessionKey("aabbccddeeff00112233445566778899")
	if len(got) != len("vireo-")+32 {
		t.Fatalf("SessionKey length = %d, want %d (%q)", len(got), len("vireo-")+32, got)
	}
	if !strings.HasPrefix(got, "vireo-") {
		t.Fatalf("SessionKey(%q) = %q, want a vireo- prefix", "…", got)
	}
	// Deterministic: the same id must always land on the same session.
	if second := SessionKey("aabbccddeeff00112233445566778899"); second != got {
		t.Fatalf("SessionKey is not deterministic: %q vs %q", got, second)
	}
}

// TestSessionKeyIsCaseSafe covers the gateway's lowercasing. Two vireo ids that
// differ only by case must not collapse into one gateway session.
func TestSessionKeyIsCaseSafe(t *testing.T) {
	upper := SessionKey("AABBCCDDEEFF00112233445566778899")
	lower := SessionKey("aabbccddeeff00112233445566778899")
	if upper == lower {
		t.Fatalf("case-differing session ids produced the same key %q", upper)
	}
	for _, k := range []string{upper, lower} {
		if k != strings.ToLower(k) {
			t.Fatalf("key %q is not lowercase; the gateway would fold it", k)
		}
		if strings.ContainsAny(k, ": ") {
			t.Fatalf("key %q contains a character that could hit a reserved namespace", k)
		}
	}
}

// TestSessionKeyAvoidsReservedNamespaces guards the gateway's rejection of
// subagent:/cron:/acp: prefixed session keys.
func TestSessionKeyAvoidsReservedNamespaces(t *testing.T) {
	for _, id := range []string{"subagent:x", "cron:y", "acp:z"} {
		k := SessionKey(id)
		for _, reserved := range []string{"subagent:", "cron:", "acp:"} {
			if strings.Contains(k, reserved) {
				t.Fatalf("SessionKey(%q) = %q, which contains reserved %q", id, k, reserved)
			}
		}
	}
}

// newTestBridge builds a bridge pointed at a stub gateway, skipping probe by
// constructing the struct directly (New would dial).
func newTestBridge(t *testing.T, gatewayURL string) *Bridge {
	t.Helper()
	return &Bridge{
		cfg: Config{
			BaseURL: gatewayURL,
			Token:   "test-token",
			Model:   "openclaw/default",
			Agents: map[string]Agent{
				"concierge": {ID: "main"},
			},
			Logger: testLogger(t),
		},
		http: &http.Client{},
	}
}

// TestHandleMessageReusesSessionAcrossTurns is the core contract: turn two must
// carry the same `user` value as turn one, or the gateway starts a new
// conversation. It also asserts the history is NOT resent.
func TestHandleMessageReusesSessionAcrossTurns(t *testing.T) {
	var seen []completionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var req completionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		seen = append(seen, req)
		writeCompletion(w, "answer "+itoa(len(seen)))
	}))
	defer srv.Close()

	b := newTestBridge(t, srv.URL)
	post := func(msg string) *httptest.ResponseRecorder {
		body := `{"agent":"concierge","session_id":"sess-1","message":"` + msg + `"}`
		rec := httptest.NewRecorder()
		b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vireo/message", strings.NewReader(body)))
		return rec
	}

	post("first")
	post("second")

	if len(seen) != 2 {
		t.Fatalf("gateway saw %d requests, want 2", len(seen))
	}
	if seen[0].User == "" {
		t.Fatal("turn 1 sent no user, so the gateway cannot keep a session")
	}
	if seen[0].User != seen[1].User {
		t.Fatalf("user changed between turns: %q then %q — the conversation would reset", seen[0].User, seen[1].User)
	}
	if len(seen[1].Messages) != 1 {
		t.Fatalf("turn 2 sent %d messages, want 1 (history lives on the gateway, not here)", len(seen[1].Messages))
	}
	if seen[1].Messages[0].Content != "second" {
		t.Fatalf("turn 2 sent %q, want the new message only", seen[1].Messages[0].Content)
	}
}

// TestHandleMessageStatelessSendsNoUser covers an agent with sessions disabled:
// vireo sends no session id, so the bridge must let the gateway mint a one-shot
// session rather than sharing one.
func TestHandleMessageStatelessSendsNoUser(t *testing.T) {
	var seen completionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		writeCompletion(w, "ok")
	}))
	defer srv.Close()

	b := newTestBridge(t, srv.URL)
	body := `{"agent":"concierge","message":"hello"}`
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vireo/message", strings.NewReader(body)))

	if seen.User != "" {
		t.Fatalf("stateless turn sent user=%q, want empty so the gateway mints a fresh session", seen.User)
	}
}

// TestHandleMessageStartMessage covers the greeting the vireo client already
// showed. It must reach the agent as context, or the agent repeats itself.
func TestHandleMessageStartMessage(t *testing.T) {
	var seen completionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&seen)
		writeCompletion(w, "ok")
	}))
	defer srv.Close()

	b := newTestBridge(t, srv.URL)
	body := `{"agent":"concierge","session_id":"s","message":"hi","initial_assistant_message":"Hello there!"}`
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vireo/message", strings.NewReader(body)))

	if len(seen.Messages) != 2 {
		t.Fatalf("sent %d messages, want 2 (greeting context + the turn)", len(seen.Messages))
	}
	if !strings.Contains(seen.Messages[0].Content, "Hello there!") {
		t.Fatalf("first message does not carry the greeting: %q", seen.Messages[0].Content)
	}
	if seen.Messages[1].Content != "hi" {
		t.Fatalf("second message = %q, want the user turn", seen.Messages[1].Content)
	}
}

// TestHandleMessageStreamsPartial covers the path vireo actually uses: it
// always sends Accept: application/x-ndjson, so a progress line must precede
// the answer.
func TestHandleMessageStreamsPartial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeCompletion(w, "the answer")
	}))
	defer srv.Close()

	b := newTestBridge(t, srv.URL)
	body := `{"agent":"concierge","session_id":"s","message":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/vireo/message", strings.NewReader(body))
	req.Header.Set("Accept", "application/x-ndjson")
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/x-ndjson") {
		t.Fatalf("Content-Type = %q, want ndjson", ct)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2 (progress then answer):\n%s", len(lines), rec.Body.String())
	}
	if !strings.Contains(lines[0], `"partial"`) {
		t.Fatalf("line 1 is not a progress line: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"reply":"the answer"`) {
		t.Fatalf("line 2 is not the answer: %s", lines[1])
	}
	if !strings.Contains(lines[1], `"session"`) {
		t.Fatalf("line 2 carries no session summary: %s", lines[1])
	}
}

// TestHandleMessageGatewayError covers a gateway failure turning into an ndjson
// error line rather than a hang, because the headers are already sent.
func TestHandleMessageGatewayError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"message": "session archived", "type": "invalid_request_error"},
		})
	}))
	defer srv.Close()

	b := newTestBridge(t, srv.URL)
	body := `{"agent":"concierge","session_id":"s","message":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/vireo/message", strings.NewReader(body))
	req.Header.Set("Accept", "application/x-ndjson")
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, req)

	out := rec.Body.String()
	if !strings.Contains(out, "session archived") {
		t.Fatalf("error detail missing from the stream:\n%s", out)
	}
	if !strings.Contains(out, `"error"`) {
		t.Fatalf("no error line:\n%s", out)
	}
}

// TestHandleMessageUnknownAgent covers a payload naming an unmapped agent.
func TestHandleMessageUnknownAgent(t *testing.T) {
	b := newTestBridge(t, "http://127.0.0.1:1")
	body := `{"agent":"nobody","session_id":"s","message":"hi"}`
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vireo/message", strings.NewReader(body)))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// TestHandleMessageEmptyReplyIsAnError covers a gateway that returns 200 with no
// content. Passing that through would show the caller a blank answer.
func TestHandleMessageEmptyReplyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "  "}}},
		})
	}))
	defer srv.Close()

	b := newTestBridge(t, srv.URL)
	rec := httptest.NewRecorder()
	b.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/vireo/message",
		strings.NewReader(`{"agent":"concierge","session_id":"s","message":"hi"}`)))

	if !strings.Contains(rec.Body.String(), "empty answer") {
		t.Fatalf("a blank gateway answer was passed through:\n%s", rec.Body.String())
	}
}

// TestNewRejectsMissingToken / the config guards, so a misconfigured bridge
// fails at startup with a named cause rather than at the first message.
func TestNewRejectsMissingToken(t *testing.T) {
	_, err := New(Config{
		BaseURL: "http://127.0.0.1:1",
		Agents:  map[string]Agent{"a": {ID: "main"}},
	})
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("err = %v, want a token complaint", err)
	}
}

func TestNewRejectsAgentWithoutID(t *testing.T) {
	_, err := New(Config{
		BaseURL: "http://127.0.0.1:1",
		Token:   "t",
		Agents:  map[string]Agent{"a": {}},
	})
	if err == nil || !strings.Contains(err.Error(), "no openclaw agent id") {
		t.Fatalf("err = %v, want a missing-id complaint", err)
	}
}

// TestNewNamesTheEnableFix pins the startup error for the default-off endpoint.
// This is the trap: chat completions is disabled unless enabled explicitly.
func TestNewNamesTheEnableFix(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	_, err := New(Config{
		BaseURL: srv.URL,
		Token:   "t",
		Agents:  map[string]Agent{"a": {ID: "main"}},
	})
	if err == nil {
		t.Fatal("want an error when /v1/models is 404")
	}
	if !strings.Contains(err.Error(), "chatCompletions.enabled") {
		t.Fatalf("error does not name the fix: %v", err)
	}
}

func TestNewRejectsBadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := New(Config{
		BaseURL: srv.URL,
		Token:   "wrong",
		Agents:  map[string]Agent{"a": {ID: "main"}},
	})
	if err == nil || !strings.Contains(err.Error(), "gateway.auth.token") {
		t.Fatalf("err = %v, want it to point at gateway.auth.token", err)
	}
}

func TestNewAcceptsHealthyGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "openclaw/default"}, {"id": "openclaw/main"}},
		})
	}))
	defer srv.Close()

	b, err := New(Config{
		BaseURL: srv.URL,
		Token:   "t",
		Agents:  map[string]Agent{"concierge": {ID: "main"}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := b.cfg.Model; got != "openclaw/default" {
		t.Fatalf("model = %q, want the stable default alias", got)
	}
}

// --- helpers ---------------------------------------------------------------

func writeCompletion(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":    "chatcmpl_test",
		"model": "openclaw/default",
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
