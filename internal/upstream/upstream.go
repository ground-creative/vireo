// Package upstream routes messages to the business agents behind an endpoint.
// A Client is one configured agent; the Registry maps agent names to clients.
//
// The only implemented transport is an HTTP webhook: vireo POSTs
// {agent, session_id, message} and reads back {reply, session}.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"vireo/internal/protocol"
	"vireo/internal/session"
)

const maxResponseBytes = 8 << 20

// Call is one message to forward.
type Call struct {
	Message   string
	SessionID string
	// InitialAssistantMessage is the agent's own greeting, sent on the first
	// turn of a conversation so the backend knows what was already said. It
	// is empty on every later turn.
	InitialAssistantMessage string
}

// Client is one agent behind the endpoint.
type Client interface {
	Name() string
	Description() string
	// StartMessage is the greeting the agent offers when a conversation
	// starts. It is delivered to the caller directly; the backend never
	// produces it.
	StartMessage() string
	// SessionsEnabled reports whether the agent holds multi-turn
	// conversations. A stateless agent answers single messages and records
	// nothing.
	SessionsEnabled() bool
	// Mint generates session ids for this agent's resolved rules.
	Mint() *session.Mint
	// TTL is the agent's resolved session idle-expiry.
	TTL() time.Duration
	Send(ctx context.Context, call Call) (protocol.MessageResponse, error)
}

// Spec is the configuration shape for one agent.
type Spec struct {
	Name         string
	Description  string
	StartMessage string
	Transport    string
	Sessions     bool
	Mint         *session.Mint
	TTL          time.Duration
	Timeout      time.Duration
}

// New builds an agent client from a spec, rejecting unsupported or
// unimplemented transports instead of dialing them hopefully.
func New(spec Spec) (Client, error) {
	if strings.TrimSpace(spec.Name) == "" {
		return nil, errors.New("upstream: agent name is required")
	}
	mint := spec.Mint
	if mint == nil {
		m, err := session.NewMint("", session.DefaultLength, session.CharsetAlnum, "")
		if err != nil {
			return nil, fmt.Errorf("upstream %s: %w", spec.Name, err)
		}
		mint = m
	}
	ttl := spec.TTL
	if ttl <= 0 {
		ttl = time.Hour
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	u, err := url.Parse(strings.TrimSpace(spec.Transport))
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("upstream %s: invalid transport %q", spec.Name, spec.Transport)
	}
	switch u.Scheme {
	case "http", "https":
		return &webhook{
			name:         spec.Name,
			startMessage: spec.StartMessage,
			description:  spec.Description,
			sessions:     spec.Sessions,
			mint:         mint,
			ttl:          ttl,
			endpoint:     u.String(),
			http:         &http.Client{Timeout: timeout},
		}, nil
	case "unix", "grpc":
		return nil, fmt.Errorf("upstream %s: transport %q is not implemented yet, use http(s)://host/path", spec.Name, u.Scheme)
	default:
		return nil, fmt.Errorf("upstream %s: unsupported transport scheme %q", spec.Name, u.Scheme)
	}
}

type webhook struct {
	name         string
	description  string
	startMessage string
	sessions     bool
	mint         *session.Mint
	ttl          time.Duration
	endpoint     string
	http         *http.Client
}

func (w *webhook) Name() string          { return w.name }
func (w *webhook) Description() string   { return w.description }
func (w *webhook) StartMessage() string  { return w.startMessage }
func (w *webhook) SessionsEnabled() bool { return w.sessions }
func (w *webhook) Mint() *session.Mint   { return w.mint }
func (w *webhook) TTL() time.Duration    { return w.ttl }

func (w *webhook) Send(ctx context.Context, call Call) (protocol.MessageResponse, error) {
	body, err := json.Marshal(struct {
		Agent                   string `json:"agent"`
		SessionID               string `json:"session_id,omitempty"`
		Message                 string `json:"message"`
		InitialAssistantMessage string `json:"initial_assistant_message,omitempty"`
	}{w.name, call.SessionID, call.Message, call.InitialAssistantMessage})
	if err != nil {
		return protocol.MessageResponse{}, fmt.Errorf("upstream %s: encode request: %w", w.name, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.endpoint, bytes.NewReader(body))
	if err != nil {
		return protocol.MessageResponse{}, fmt.Errorf("upstream %s: %w", w.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Vireo-Protocol", protocol.Version)
	req.Header.Set("X-Vireo-Agent", w.name)
	if call.SessionID != "" {
		req.Header.Set("X-Vireo-Session", call.SessionID)
	}

	resp, err := w.http.Do(req)
	if err != nil {
		return protocol.MessageResponse{}, fmt.Errorf("upstream %s: %w", w.name, err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return protocol.MessageResponse{}, fmt.Errorf("upstream %s: read response: %w", w.name, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return protocol.MessageResponse{}, fmt.Errorf("upstream %s: %s: %s", w.name, resp.Status, snippet(data))
	}

	// Lenient heuristic: a body that looks like {reply, session} is treated
	// as the envelope; anything else is taken as a plain reply string.
	var envelope protocol.MessageResponse
	if err := json.Unmarshal(data, &envelope); err == nil && (envelope.Reply != "" || envelope.Session != nil) {
		return envelope, nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		return protocol.MessageResponse{Reply: text}, nil
	}
	return protocol.MessageResponse{Reply: strings.TrimSpace(string(data))}, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// Registry routes agent names to agent clients.
type Registry struct {
	byAgent map[string]Client
}

func NewRegistry() *Registry {
	return &Registry{byAgent: map[string]Client{}}
}

// Add registers an agent, rejecting duplicate agent names.
func (r *Registry) Add(c Client) error {
	if _, ok := r.byAgent[c.Name()]; ok {
		return fmt.Errorf("agent %q is configured twice", c.Name())
	}
	r.byAgent[c.Name()] = c
	return nil
}

// LookupAgent finds a client by agent name.
func (r *Registry) LookupAgent(name string) (Client, bool) {
	c, ok := r.byAgent[name]
	return c, ok
}

// Agents returns the registered agents ordered by name.
func (r *Registry) Agents() []Client {
	out := make([]Client, 0, len(r.byAgent))
	for _, c := range r.byAgent {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func (r *Registry) Len() int { return len(r.byAgent) }
