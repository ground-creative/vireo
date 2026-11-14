package server

import (
	"crypto/tls"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"vireo/internal/frame"
	"vireo/internal/protocol"
	"vireo/internal/session"
	"vireo/internal/upstream"
)

// backend records every webhook body the daemon sends.
type backend struct {
	bodies []map[string]any
}

func (b *backend) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("backend got a body that is not JSON: %v", err)
		}
		b.bodies = append(b.bodies, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"reply":"ok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func (b *backend) last(t *testing.T) map[string]any {
	t.Helper()
	if len(b.bodies) == 0 {
		t.Fatal("the backend was never called")
	}
	return b.bodies[len(b.bodies)-1]
}

// start brings a daemon up on a loopback port and returns its address.
func start(t *testing.T, registry *upstream.Registry) string {
	t.Helper()
	srv := New(Config{
		TLS:          &tls.Config{NextProtos: []string{protocol.Version}},
		Manifest:     protocol.Manifest{Protocol: protocol.Version},
		Registry:     registry,
		Sessions:     session.NewMemory(),
		ListSessions: true,
		CallTimeout:  5 * time.Second,
		Logger:       log.New(io.Discard, "", 0),
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Close)
	return ln.Addr().String()
}

// dial connects and negotiates, returning the frame reader.
func dial(t *testing.T, addr string) (net.Conn, *frame.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	rd := frame.NewReader(conn)
	write(t, conn, protocol.TypeNegotiate, 1, protocol.NegotiateRequest{
		Protocol: protocol.Version,
		Client:   protocol.ClientInfo{ID: "vireo_0123456789abcdef0123456789abcdef", Name: "test"},
	})
	f := read(t, rd)
	if f.Type != protocol.TypeManifest {
		t.Fatalf("first answer is %s, want manifest", protocol.TypeName(f.Type))
	}
	return conn, rd
}

func write(t *testing.T, conn net.Conn, typ uint8, stream uint32, payload any) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := frame.Write(conn, frame.Frame{StreamID: stream, Type: typ, Payload: raw}); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

func read(t *testing.T, rd *frame.Reader) frame.Frame {
	t.Helper()
	f, err := rd.Read()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	return f
}

// data reads the data frame of one exchange and the end frame that follows.
func data(t *testing.T, rd *frame.Reader) []byte {
	t.Helper()
	f := read(t, rd)
	if f.Type == protocol.TypeError {
		t.Fatalf("error frame: %s", string(f.Payload))
	}
	if f.Type != protocol.TypeData {
		t.Fatalf("answer is %s, want data", protocol.TypeName(f.Type))
	}
	end := read(t, rd)
	if end.Type != protocol.TypeEnd {
		t.Fatalf("answer ends with %s, want end", protocol.TypeName(end.Type))
	}
	return f.Payload
}

// A greeting comes back with the session, without asking the backend for it.
func TestSessionNewReturnsTheStartMessage(t *testing.T) {
	be := &backend{}
	registry := upstream.NewRegistry()
	agent, err := upstream.New(upstream.Spec{
		Name:         "concierge",
		Transport:    be.serve(t),
		StartMessage: "Hello there",
		Sessions:     true,
	})
	if err != nil {
		t.Fatalf("upstream.New: %v", err)
	}
	if err := registry.Add(agent); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	conn, rd := dial(t, start(t, registry))
	write(t, conn, protocol.TypeSession, 2, protocol.SessionRequest{
		Op:    protocol.SessionOpNew,
		Agent: "concierge",
	})

	var started protocol.NewSessionResponse
	if err := json.Unmarshal(data(t, rd), &started); err != nil {
		t.Fatalf("decode session.new answer: %v", err)
	}
	if started.StartMessage != "Hello there" {
		t.Errorf("start_message = %q, want %q", started.StartMessage, "Hello there")
	}
	if started.Session.ID == "" {
		t.Error("session.new returned no session id")
	}
	if len(be.bodies) != 0 {
		t.Errorf("the backend was called %d times for the greeting, want none", len(be.bodies))
	}
}

// The first turn tells the backend what the agent already said, so the model
// does not repeat the greeting. Later turns leave it out.
func TestFirstTurnCarriesTheStartMessage(t *testing.T) {
	be := &backend{}
	registry := upstream.NewRegistry()
	agent, err := upstream.New(upstream.Spec{
		Name:         "concierge",
		Transport:    be.serve(t),
		StartMessage: "Hello there",
		Sessions:     true,
	})
	if err != nil {
		t.Fatalf("upstream.New: %v", err)
	}
	if err := registry.Add(agent); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	conn, rd := dial(t, start(t, registry))
	write(t, conn, protocol.TypeSession, 2, protocol.SessionRequest{
		Op:    protocol.SessionOpNew,
		Agent: "concierge",
	})
	var started protocol.NewSessionResponse
	if err := json.Unmarshal(data(t, rd), &started); err != nil {
		t.Fatalf("decode session.new answer: %v", err)
	}

	write(t, conn, protocol.TypeMessage, 3, protocol.MessageRequest{
		Agent:     "concierge",
		SessionID: started.Session.ID,
		Message:   "hello",
	})
	data(t, rd)
	body := be.last(t)
	got, ok := body["initial_assistant_message"].(string)
	if !ok {
		t.Fatalf("first turn body has no initial_assistant_message: %v", body)
	}
	if got != "Hello there" {
		t.Errorf("initial_assistant_message = %q, want %q", got, "Hello there")
	}

	write(t, conn, protocol.TypeMessage, 4, protocol.MessageRequest{
		Agent:     "concierge",
		SessionID: started.Session.ID,
		Message:   "and again",
	})
	data(t, rd)
	body = be.last(t)
	if v, present := body["initial_assistant_message"]; present {
		t.Errorf("second turn carries initial_assistant_message=%v, want it absent", v)
	}
}

// An agent without a greeting behaves as before: no field, no extra call.
func TestFirstTurnHasNoStartMessageWhenNotConfigured(t *testing.T) {
	be := &backend{}
	registry := upstream.NewRegistry()
	agent, err := upstream.New(upstream.Spec{
		Name:      "orders",
		Transport: be.serve(t),
		Sessions:  true,
	})
	if err != nil {
		t.Fatalf("upstream.New: %v", err)
	}
	if err := registry.Add(agent); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	conn, rd := dial(t, start(t, registry))
	write(t, conn, protocol.TypeSession, 2, protocol.SessionRequest{
		Op:    protocol.SessionOpNew,
		Agent: "orders",
	})
	var started protocol.NewSessionResponse
	if err := json.Unmarshal(data(t, rd), &started); err != nil {
		t.Fatalf("decode session.new answer: %v", err)
	}
	if started.StartMessage != "" {
		t.Errorf("start_message = %q, want it empty", started.StartMessage)
	}

	write(t, conn, protocol.TypeMessage, 3, protocol.MessageRequest{
		Agent:     "orders",
		SessionID: started.Session.ID,
		Message:   "hello",
	})
	data(t, rd)
	body := be.last(t)
	if v, present := body["initial_assistant_message"]; present {
		t.Errorf("body carries initial_assistant_message=%v, want it absent", v)
	}
}

// blockingBackend holds every request open until its context is cancelled, and
// records that the cancellation arrived. It is how a slow model call looks to
// the daemon.
type blockingBackend struct {
	arrived  chan struct{}
	canceled chan struct{}
}

func (b *blockingBackend) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// The body has to be consumed before net/http starts watching the
		// connection, and it is that watch that reports a cancelled request.
		_, _ = io.ReadAll(req.Body)
		select {
		case b.arrived <- struct{}{}:
		default:
		}
		<-req.Context().Done()
		select {
		case b.canceled <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A cancel frame abandons the turn in flight: the backend's request context is
// cancelled, the caller gets an end frame marked cancelled, and the turn is not
// counted. This is the regression for the blocking read loop — before it, the
// cancel frame could not be read until the turn had already finished.
func TestCancelAbandonsTheTurn(t *testing.T) {
	be := &blockingBackend{
		arrived:  make(chan struct{}, 1),
		canceled: make(chan struct{}, 1),
	}
	registry := upstream.NewRegistry()
	agent, err := upstream.New(upstream.Spec{
		Name:      "orders",
		Transport: be.serve(t),
		Sessions:  true,
	})
	if err != nil {
		t.Fatalf("upstream.New: %v", err)
	}
	if err := registry.Add(agent); err != nil {
		t.Fatalf("registry.Add: %v", err)
	}

	conn, rd := dial(t, start(t, registry))
	write(t, conn, protocol.TypeSession, 2, protocol.SessionRequest{
		Op:    protocol.SessionOpNew,
		Agent: "orders",
	})
	var started protocol.NewSessionResponse
	if err := json.Unmarshal(data(t, rd), &started); err != nil {
		t.Fatalf("decode session.new answer: %v", err)
	}

	write(t, conn, protocol.TypeMessage, 3, protocol.MessageRequest{
		Agent:     "orders",
		SessionID: started.Session.ID,
		Message:   "build me an assistant",
	})

	// Wait until the turn is genuinely in flight, so the cancel cannot race the
	// goroutine that registers it.
	select {
	case <-be.arrived:
	case <-time.After(5 * time.Second):
		t.Fatal("the backend was never called")
	}

	write(t, conn, protocol.TypeCancel, 3, nil)

	select {
	case <-be.canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("the backend's request was not cancelled")
	}

	f := read(t, rd)
	if f.Type != protocol.TypeEnd {
		t.Fatalf("cancelled turn ends with %s, want end", protocol.TypeName(f.Type))
	}
	var end protocol.EndPayload
	if err := json.Unmarshal(f.Payload, &end); err != nil {
		t.Fatalf("decode end: %v", err)
	}
	if end.OK {
		t.Error("cancelled turn reports ok=true")
	}
	if !end.Cancelled {
		t.Error("end frame is not marked cancelled")
	}

	// An abandoned turn is not a turn: the envelope must not move.
	write(t, conn, protocol.TypeSession, 4, protocol.SessionRequest{
		Op:    protocol.SessionOpList,
		Agent: "orders",
	})
	var list protocol.ListSessionsResponse
	if err := json.Unmarshal(data(t, rd), &list); err != nil {
		t.Fatalf("decode session.list answer: %v", err)
	}
	if len(list.Sessions) != 1 {
		t.Fatalf("session.list returned %d sessions, want 1", len(list.Sessions))
	}
	if list.Sessions[0].Turns != 0 {
		t.Errorf("turns = %d, want 0 for a cancelled turn", list.Sessions[0].Turns)
	}
}
