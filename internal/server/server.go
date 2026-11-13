// Package server accepts agent connections over TLS (TCP) and QUIC (UDP)
// and drives the conversation:
//
//	negotiate (client id required) → manifest (endpoint, agents, refs)
//	→ message (agent, optional session id, text) → data + end
//	→ session frame (new / list / get / close / prune)
//
// Messages are handled sequentially per connection; a disconnect during a
// long call lets it finish or time out rather than tearing it down mid-flight.
package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"

	"vireo/internal/frame"
	"vireo/internal/protocol"
	"vireo/internal/session"
	"vireo/internal/upstream"
)

// Config wires the server together.
type Config struct {
	TLS          *tls.Config
	Manifest     protocol.Manifest
	Registry     *upstream.Registry
	Sessions     session.Store
	ListSessions bool
	Retention    time.Duration
	CallTimeout  time.Duration
	Logger       *log.Logger
}

// Server owns the listeners.
type Server struct {
	cfg     Config
	log     *log.Logger
	mu      sync.Mutex
	closers []io.Closer
}

func New(cfg Config) *Server {
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 30 * time.Second
	}
	return &Server{cfg: cfg, log: cfg.Logger}
}

func (s *Server) track(c io.Closer) {
	s.mu.Lock()
	s.closers = append(s.closers, c)
	s.mu.Unlock()
}

// Close shuts the listeners down. Active connections drain on their own.
func (s *Server) Close() {
	s.mu.Lock()
	closers := append([]io.Closer(nil), s.closers...)
	s.mu.Unlock()
	for _, c := range closers {
		_ = c.Close()
	}
}

// Serve accepts TLS-wrapped TCP connections.
func (s *Server) Serve(l net.Listener) error {
	s.track(l)
	s.log.Printf("listening on %s (tcp/tls, alpn=%s)", l.Addr(), strings.Join(s.cfg.TLS.NextProtos, ","))
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		go s.handleConn(conn, conn.RemoteAddr().String())
	}
}

// ServeQUIC accepts QUIC connections; addr is logged since the listener does
// not expose it.
func (s *Server) ServeQUIC(l *quic.Listener, addr string) error {
	s.track(l)
	s.log.Printf("listening on %s (quic, alpn=%s)", addr, strings.Join(s.cfg.TLS.NextProtos, ","))
	for {
		conn, err := l.Accept(context.Background())
		if err != nil {
			return err
		}
		go s.handleQUICConn(conn)
	}
}

func (s *Server) handleQUICConn(conn quic.Connection) {
	remote := conn.RemoteAddr().String()
	defer conn.CloseWithError(0, "")
	for {
		stream, err := conn.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go s.handleConn(stream, remote)
	}
}

// errCleanClose marks a connection that went away before negotiating.
var errCleanClose = errors.New("closed before negotiate")

func (s *Server) handleConn(rw io.ReadWriteCloser, remote string) {
	defer rw.Close()

	c := &connState{
		srv:    s,
		rw:     rw,
		rd:     frame.NewReader(rw),
		remote: remote,
	}
	s.log.Printf("[%s] connection opened", remote)
	defer s.log.Printf("[%s] connection closed", remote)

	if err := c.handshake(); err != nil {
		if !errors.Is(err, errCleanClose) {
			s.log.Printf("[%s] rejected: %v", remote, err)
		}
		return
	}
	c.loop()
}

type connState struct {
	srv    *Server
	rw     io.ReadWriteCloser
	rd     *frame.Reader
	remote string

	writeMu sync.Mutex

	clientID   string
	clientName string
}

func (c *connState) write(f frame.Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return frame.Write(c.rw, f)
}

func (c *connState) writeJSON(stream uint32, typ uint8, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		c.srv.log.Printf("[%s] marshal frame %s: %v", c.remote, protocol.TypeName(typ), err)
		return
	}
	if err := c.write(frame.Frame{StreamID: stream, Type: typ, Payload: payload}); err != nil {
		c.srv.log.Printf("[%s] write frame %s: %v", c.remote, protocol.TypeName(typ), err)
	}
}

func (c *connState) writeError(stream uint32, code, msg string) {
	c.srv.log.Printf("[%s] error stream=%d %s: %s", c.remote, stream, code, msg)
	c.writeJSON(stream, protocol.TypeError, protocol.ErrorPayload{Code: code, Message: msg})
}

// respond sends a single data frame followed by end.
func (c *connState) respond(stream uint32, v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		c.writeError(stream, protocol.CodeInternal, err.Error())
		return
	}
	if err := c.write(frame.Frame{StreamID: stream, Type: protocol.TypeData, Payload: payload}); err != nil {
		return
	}
	_ = c.write(frame.Frame{StreamID: stream, Type: protocol.TypeEnd, Payload: []byte(`{"ok":true}`)})
}

// respondEnd sends a data frame followed by an end frame carrying the updated
// session envelope.
func (c *connState) respondEnd(stream uint32, v any, info *protocol.SessionInfo) {
	payload, err := json.Marshal(v)
	if err != nil {
		c.writeError(stream, protocol.CodeInternal, err.Error())
		return
	}
	if err := c.write(frame.Frame{StreamID: stream, Type: protocol.TypeData, Payload: payload}); err != nil {
		return
	}
	end, err := json.Marshal(protocol.EndPayload{OK: true, Session: info})
	if err != nil {
		end = []byte(`{"ok":true}`)
	}
	_ = c.write(frame.Frame{StreamID: stream, Type: protocol.TypeEnd, Payload: end})
}

func (c *connState) handshake() error {
	f, err := c.rd.Read()
	if err != nil {
		return errCleanClose
	}
	if f.Type != protocol.TypeNegotiate {
		c.writeError(f.StreamID, protocol.CodeInvalidFrame, "first frame must be negotiate")
		return fmt.Errorf("first frame is %s, not negotiate", protocol.TypeName(f.Type))
	}
	var req protocol.NegotiateRequest
	if len(f.Payload) > 0 {
		if err := json.Unmarshal(f.Payload, &req); err != nil {
			c.writeError(f.StreamID, protocol.CodeInvalidFrame, "malformed negotiate payload: "+err.Error())
			return err
		}
	}
	if req.Protocol != "" && req.Protocol != protocol.Version {
		c.writeError(f.StreamID, protocol.CodeBadProtocol, fmt.Sprintf("unsupported protocol %q (want %q)", req.Protocol, protocol.Version))
		return fmt.Errorf("unsupported protocol %q", req.Protocol)
	}
	id := strings.TrimSpace(req.Client.ID)
	if err := protocol.ValidateClientID(id); err != nil {
		c.writeError(f.StreamID, protocol.CodeBadClientID, err.Error())
		return fmt.Errorf("client id %q: %w", id, err)
	}
	c.clientID = id
	c.clientName = req.Client.Name
	c.srv.log.Printf("[%s] negotiate client=%q (%s)", c.remote, c.clientID, c.clientName)
	c.writeJSON(f.StreamID, protocol.TypeManifest, c.srv.cfg.Manifest)
	return nil
}

func (c *connState) loop() {
	for {
		f, err := c.rd.Read()
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				c.srv.log.Printf("[%s] read: %v", c.remote, err)
			}
			return
		}
		switch f.Type {
		case protocol.TypeMessage:
			c.handleMessage(f)
		case protocol.TypeSession:
			c.handleSession(f)
		case protocol.TypeNegotiate:
			c.srv.log.Printf("[%s] re-negotiate", c.remote)
			c.writeJSON(f.StreamID, protocol.TypeManifest, c.srv.cfg.Manifest)
		default:
			c.writeError(f.StreamID, protocol.CodeInvalidFrame, "unexpected frame "+protocol.TypeName(f.Type))
		}
	}
}

// handleMessage routes one conversation turn to its agent.
func (c *connState) handleMessage(f frame.Frame) {
	var req protocol.MessageRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		c.writeError(f.StreamID, protocol.CodeInvalidFrame, "malformed message payload: "+err.Error())
		return
	}
	req.Agent = strings.TrimSpace(req.Agent)
	req.Message = strings.TrimSpace(req.Message)
	if req.Agent == "" {
		c.writeError(f.StreamID, protocol.CodeInvalidFrame, "message requires an agent")
		return
	}
	if req.Message == "" {
		c.writeError(f.StreamID, protocol.CodeInvalidFrame, "message requires text")
		return
	}

	client, ok := c.srv.cfg.Registry.LookupAgent(req.Agent)
	if !ok {
		c.writeError(f.StreamID, protocol.CodeAgentNotFound, fmt.Sprintf("no agent named %q", req.Agent))
		return
	}

	sessionID := strings.TrimSpace(req.SessionID)
	var info *protocol.SessionInfo
	if sessionID != "" {
		if sessionID = strings.TrimSpace(req.SessionID); len(sessionID) > 128 {
			c.writeError(f.StreamID, protocol.CodeInvalidFrame, "session id is too long")
			return
		}
		envelope, ok := c.srv.cfg.Sessions.Get(c.clientID, client.Name(), sessionID)
		if !ok {
			c.writeError(f.StreamID, protocol.CodeUnknownSession,
				fmt.Sprintf("no session %q for agent %q — it may have been closed; list yours or start a new one", sessionID, client.Name()))
			return
		}
		info = &envelope
	} else if client.SessionsEnabled() {
		// A conversational agent needs a session: the envelope is where the
		// turn count and summary live.
		c.writeError(f.StreamID, protocol.CodeSessionRequired,
			fmt.Sprintf("agent %q holds conversations: start one first (session.new)", client.Name()))
		return
	}

	c.forward(f.StreamID, client, req, sessionID, info)
}

// forward performs the upstream call and answers with data + end.
func (c *connState) forward(stream uint32, client upstream.Client, req protocol.MessageRequest, sessionID string, info *protocol.SessionInfo) {
	ctx, cancel := context.WithTimeout(context.Background(), c.srv.cfg.CallTimeout)
	defer cancel()

	c.srv.log.Printf("[%s] message stream=%d agent=%q session=%q: %s",
		c.remote, stream, client.Name(), sessionID, compact(req.Message))
	started := time.Now()
	resp, err := client.Send(ctx, upstream.Call{Message: req.Message, SessionID: sessionID})
	if err != nil {
		c.srv.log.Printf("[%s] message stream=%d agent=%q failed in %s: %v",
			c.remote, stream, client.Name(), time.Since(started).Round(time.Millisecond), err)
		c.writeError(stream, protocol.CodeUpstreamError, err.Error())
		return
	}

	var envelope *protocol.SessionInfo
	if sessionID != "" && info != nil {
		summary := info.Summary
		if resp.Session != nil && resp.Session.Summary != "" {
			summary = resp.Session.Summary
		}
		updated, ok := c.srv.cfg.Sessions.Touch(c.clientID, client.Name(), sessionID, "message", summary, client.TTL())
		if ok {
			envelope = &updated
			if resp.Session != nil && resp.Session.Status == protocol.StatusClosed {
				if closed, ok := c.srv.cfg.Sessions.Close(c.clientID, client.Name(), sessionID); ok {
					envelope = &closed
				}
			}
		} else {
			envelope = info
		}
	}

	c.srv.log.Printf("[%s] ok stream=%d agent=%q session=%q in %s",
		c.remote, stream, client.Name(), sessionID, time.Since(started).Round(time.Millisecond))

	payload, err := json.Marshal(protocol.MessageResponse{Reply: resp.Reply, Session: resp.Session})
	if err != nil {
		c.writeError(stream, protocol.CodeInternal, err.Error())
		return
	}
	if err := c.write(frame.Frame{StreamID: stream, Type: protocol.TypeData, Payload: payload}); err != nil {
		return
	}
	end, err := json.Marshal(protocol.EndPayload{OK: true, Session: envelope})
	if err != nil {
		end = []byte(`{"ok":true}`)
	}
	_ = c.write(frame.Frame{StreamID: stream, Type: protocol.TypeEnd, Payload: end})
}

// handleSession answers the daemon's own session operations.
func (c *connState) handleSession(f frame.Frame) {
	var req protocol.SessionRequest
	if err := json.Unmarshal(f.Payload, &req); err != nil {
		c.writeError(f.StreamID, protocol.CodeInvalidFrame, "malformed session payload: "+err.Error())
		return
	}
	if !c.srv.cfg.ListSessions {
		c.writeError(f.StreamID, protocol.CodeInvalidFrame, "session commands are disabled")
		return
	}

	switch req.Op {
	case protocol.SessionOpNew:
		c.sessionNew(f.StreamID, req)
	case protocol.SessionOpList:
		c.sessionList(f.StreamID, req)
	case protocol.SessionOpGet:
		c.sessionGet(f.StreamID, req)
	case protocol.SessionOpClose:
		c.sessionClose(f.StreamID, req)
	case protocol.SessionOpPrune:
		c.sessionPrune(f.StreamID, req)
	default:
		c.writeError(f.StreamID, protocol.CodeInvalidFrame, fmt.Sprintf("unknown session op %q", req.Op))
	}
}

func (c *connState) sessionNew(stream uint32, req protocol.SessionRequest) {
	if req.Agent == "" {
		c.writeError(stream, protocol.CodeInvalidFrame, "session.new requires an agent")
		return
	}
	client, ok := c.srv.cfg.Registry.LookupAgent(req.Agent)
	if !ok {
		c.writeError(stream, protocol.CodeAgentNotFound, fmt.Sprintf("no agent named %q", req.Agent))
		return
	}
	if !client.SessionsEnabled() {
		c.writeError(stream, protocol.CodeInvalidFrame, fmt.Sprintf("agent %q is stateless and has no sessions", client.Name()))
		return
	}
	id, err := client.Mint().Generate()
	if err != nil {
		c.writeError(stream, protocol.CodeInternal, err.Error())
		return
	}
	info := c.srv.cfg.Sessions.Create(protocol.SessionInfo{
		ID:      id,
		Owner:   c.clientID,
		Agent:   client.Name(),
		Summary: req.Summary,
		Status:  protocol.StatusOpen,
	})
	c.srv.log.Printf("[%s] session new %s for agent=%q by client=%q", c.remote, info.ID, client.Name(), c.clientID)
	c.respondEnd(stream, info, &info)
}

func (c *connState) sessionList(stream uint32, req protocol.SessionRequest) {
	if req.Agent == "" {
		c.writeError(stream, protocol.CodeInvalidFrame, "session.list requires an agent")
		return
	}
	client, ok := c.srv.cfg.Registry.LookupAgent(req.Agent)
	if !ok {
		c.writeError(stream, protocol.CodeAgentNotFound, fmt.Sprintf("no agent named %q", req.Agent))
		return
	}
	limit := req.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	all := c.srv.cfg.Sessions.List(c.clientID, client.Name())
	start := 0
	if req.Cursor != "" {
		for i, s := range all {
			if s.ID == req.Cursor {
				start = i + 1
				break
			}
		}
	}
	if start > len(all) {
		start = len(all)
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	page := all[start:end]
	resp := protocol.ListSessionsResponse{Sessions: page}
	if end < len(all) && len(page) > 0 {
		resp.Next = page[len(page)-1].ID
	}
	if resp.Sessions == nil {
		resp.Sessions = []protocol.SessionInfo{}
	}
	c.srv.log.Printf("[%s] session list agent=%q -> %d session(s) for client=%q", c.remote, req.Agent, len(page), c.clientID)
	c.respond(stream, resp)
}

func (c *connState) sessionGet(stream uint32, req protocol.SessionRequest) {
	if req.Agent == "" || req.ID == "" {
		c.writeError(stream, protocol.CodeInvalidFrame, "session.get requires agent and id")
		return
	}
	info, ok := c.srv.cfg.Sessions.Get(c.clientID, req.Agent, req.ID)
	if !ok {
		c.writeError(stream, protocol.CodeUnknownSession, fmt.Sprintf("no session %q for agent %q", req.ID, req.Agent))
		return
	}
	c.respond(stream, info)
}

func (c *connState) sessionClose(stream uint32, req protocol.SessionRequest) {
	if req.Agent == "" || req.ID == "" {
		c.writeError(stream, protocol.CodeInvalidFrame, "session.close requires agent and id")
		return
	}
	info, ok := c.srv.cfg.Sessions.Close(c.clientID, req.Agent, req.ID)
	if !ok {
		c.writeError(stream, protocol.CodeUnknownSession, fmt.Sprintf("no session %q for agent %q", req.ID, req.Agent))
		return
	}
	c.srv.log.Printf("[%s] session close %s agent=%q by client=%q (%s)", c.remote, req.ID, req.Agent, c.clientID, req.Reason)
	c.respond(stream, info)
}

func (c *connState) sessionPrune(stream uint32, req protocol.SessionRequest) {
	retention := c.srv.cfg.Retention
	if req.Retention != "" {
		d, err := time.ParseDuration(req.Retention)
		if err != nil {
			c.writeError(stream, protocol.CodeInvalidFrame, fmt.Sprintf("retention %q is not a duration (try 720h)", req.Retention))
			return
		}
		retention = d
	}
	if retention <= 0 {
		c.writeError(stream, protocol.CodeInvalidFrame, "no retention configured; set session.retention or pass {\"retention\":\"720h\"}")
		return
	}
	before := c.srv.cfg.Sessions.Count()
	removed := c.srv.cfg.Sessions.Prune(time.Now(), retention)
	after := c.srv.cfg.Sessions.Count()
	c.srv.log.Printf("[%s] session prune retention=%s removed=%d open=%d closed=%d expired=%d",
		c.remote, retention, removed, after.Open, after.Closed, after.Expired)
	c.respond(stream, protocol.PruneResult{
		Retention: retention.String(),
		Removed:   removed,
		Before:    protocol.SessionCounts{Open: before.Open, Closed: before.Closed, Expired: before.Expired, Total: before.Total},
		After:     protocol.SessionCounts{Open: after.Open, Closed: after.Closed, Expired: after.Expired, Total: after.Total},
	})
}

func compact(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
