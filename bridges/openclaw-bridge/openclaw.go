// Package openclaw is a vireo backend whose answers come from an OpenClaw
// gateway's OpenAI-compatible Chat Completions surface.
//
// It sits behind vireo's webhook contract, exactly like cmd/fakeagent: vireo
// POSTs {agent, session_id, message} and reads back {reply, session}. What is
// different is that a model answers, so the package has to solve one problem
// the webhook contract does not: OpenClaw keeps conversation history on its
// side, keyed by something vireo does not send.
//
// That key is the OpenAI `user` string. OpenClaw derives a session key from it
// and resumes whatever history is stored under it. This package hashes vireo's
// minted session id into that value, so:
//
//   - the same vireo conversation always resumes the same OpenClaw session;
//   - a different vireo session gets a different OpenClaw session;
//   - nothing is resent between turns, because resending would duplicate the
//     history the gateway already holds.
//
// One bridge serves many vireo agents: each webhook agent name maps to its own
// OpenClaw agent, so "concierge" and "researcher" can be different agents
// without a second process.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"
)

// maxResponseBytes bounds what the bridge reads from the gateway. A chat
// completion is text; 8 MiB is far beyond any legitimate answer and stops a
// runaway response from filling memory.
const maxResponseBytes = 8 << 20

// DefaultGatewayURL is where a local OpenClaw gateway listens. The gateway
// serves the Control UI and the HTTP API on the same port.
const DefaultGatewayURL = "http://127.0.0.1:18789"

// DefaultModel is the stable agent target. openclaw/default survives a change
// of the configured default agent, so a bridge config can hardcode it.
const DefaultModel = "openclaw/default"

// Config is the gateway connection plus the map of vireo agent names to
// OpenClaw agents.
type Config struct {
	BaseURL string        // http://127.0.0.1:18789 — the gateway, not the UI
	Token   string        // bearer token: gateway.auth.token
	Model   string        // default target, e.g. openclaw/default
	Timeout time.Duration // per agent run; model turns are slow
	Logger  *log.Logger

	// Agents maps a vireo agent name to the OpenClaw agent behind it. The key
	// is the `agent` field of the webhook payload.
	Agents map[string]Agent

	// Default names the agent used when the payload's agent is not in Agents.
	// Empty means unknown agents are refused.
	Default string

	// DefaultAgent is the OpenClaw agent used when no mapping is configured at
	// all. It is what makes a config file optional: `-agent main` sets this and
	// the bridge becomes a single-agent bridge, every vireo agent name resolving
	// to that one id.
	DefaultAgent string
}

// Agent is one OpenClaw agent behind a vireo agent name.
type Agent struct {
	ID    string // OpenClaw agent id, e.g. "main"
	Label string // human name, for logs only
	Model string // optional target override, e.g. openclaw/research
}

// Bridge answers vireo's webhook with a gateway chat completion.
type Bridge struct {
	cfg  Config
	http *http.Client
}

// New validates the config, then checks the gateway actually serves the
// endpoint. The check is not politeness: chat completions is disabled by
// default, and a 404 there is a configuration step, not a network failure.
// Failing at startup with the fix named beats a 404 on every message.
func New(cfg Config) (*Bridge, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = DefaultGatewayURL
	}
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, fmt.Errorf("openclaw: token is required — it is the gateway's gateway.auth.token, not a provider API key")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		cfg.Model = DefaultModel
	}

	agents := make(map[string]Agent, len(cfg.Agents))
	for name, a := range cfg.Agents {
		name = strings.TrimSpace(name)
		id := strings.TrimSpace(a.ID)
		if name == "" {
			return nil, fmt.Errorf("openclaw: an agent is configured under an empty vireo name")
		}
		if id == "" {
			return nil, fmt.Errorf("openclaw: agent %q has no openclaw agent id", name)
		}
		agents[name] = Agent{ID: id, Label: strings.TrimSpace(a.Label), Model: strings.TrimSpace(a.Model)}
	}
	cfg.Agents = agents
	cfg.Default = strings.TrimSpace(cfg.Default)
	if cfg.Default != "" {
		if _, ok := agents[cfg.Default]; !ok {
			return nil, fmt.Errorf("openclaw: default %q is not one of the configured agents", cfg.Default)
		}
	}
	if len(agents) == 0 && strings.TrimSpace(cfg.DefaultAgent) == "" {
		return nil, fmt.Errorf("openclaw: no agent mapping — pass -agent <openclaw-agent-id> for a single agent, or list agents in a config file")
	}

	if cfg.Timeout <= 0 {
		cfg.Timeout = 180 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = log.Default()
	}

	b := &Bridge{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
	if err := b.probe(); err != nil {
		return nil, err
	}
	return b, nil
}

// probe confirms the gateway answers and that chat completions is enabled. It
// names the fix for each distinguishable failure instead of guessing.
func (b *Bridge) probe() error {
	req, err := http.NewRequest(http.MethodGet, b.cfg.BaseURL+"/v1/models", nil)
	if err != nil {
		return fmt.Errorf("openclaw: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.cfg.Token)

	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("openclaw: gateway unreachable at %s: %w", b.cfg.BaseURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("openclaw: gateway rejected the token (%s) — it must match gateway.auth.token", resp.Status)
	case http.StatusNotFound:
		return fmt.Errorf("openclaw: %s/v1/models is not served — the OpenAI-compatible surface is disabled by default; run: openclaw config set gateway.http.endpoints.chatCompletions.enabled true", b.cfg.BaseURL)
	default:
		return fmt.Errorf("openclaw: %s/v1/models: %s: %s", b.cfg.BaseURL, resp.Status, snippet(body))
	}

	// The model list proves the surface is on. A configured agent id absent
	// from it is a warning, not a failure: /v1/models lists only top-level
	// targets, and an override target may legitimately be absent.
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &listed) == nil {
		known := map[string]bool{}
		for _, m := range listed.Data {
			known[m.ID] = true
		}
		for name, a := range b.cfg.Agents {
			if a.Model != "" {
				continue // explicit override, cannot be checked here
			}
			target := "openclaw/" + a.ID
			if !known[target] && !known["openclaw"] {
				b.cfg.Logger.Printf("warning: agent %q targets %q, which /v1/models does not list", name, target)
			}
		}
	}
	return nil
}

// AgentFor resolves the OpenClaw agent behind a vireo agent name. The map wins;
// Default covers names it does not list.
func (b *Bridge) AgentFor(agent string) (Agent, bool) {
	agent = strings.TrimSpace(agent)
	if a, ok := b.cfg.Agents[agent]; ok {
		return a, true
	}
	if b.cfg.Default != "" {
		if a, ok := b.cfg.Agents[b.cfg.Default]; ok {
			return a, true
		}
	}
	// No mapping configured: the bridge is a single-agent bridge and every vireo
	// agent name resolves to that one OpenClaw agent. This is what makes
	// `./openclaw-bridge -agent main` work with no config file at all — the
	// common local case, and the shape most people start with.
	if len(b.cfg.Agents) == 0 && strings.TrimSpace(b.cfg.DefaultAgent) != "" {
		return Agent{ID: b.cfg.DefaultAgent, Label: b.cfg.DefaultAgent}, true
	}
	return Agent{}, false
}

// AgentNames returns "agent" or "agent (label)" per configured agent, sorted,
// for the startup log.
func (b *Bridge) AgentNames() []string {
	// No mapping: one OpenClaw agent answers every vireo agent name. Say so,
	// rather than logging an empty list that looks like a misconfiguration.
	if len(b.cfg.Agents) == 0 {
		if id := strings.TrimSpace(b.cfg.DefaultAgent); id != "" {
			return []string{"any → " + id}
		}
		return nil
	}
	names := make([]string, 0, len(b.cfg.Agents))
	for name, a := range b.cfg.Agents {
		if a.Label != "" {
			names = append(names, fmt.Sprintf("%s (%s)", name, a.Label))
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Handler returns the HTTP handler vireo talks to.
func (b *Bridge) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/vireo/message", b.handleMessage)
	return mux
}

// Message is vireo's webhook payload.
type Message struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
	// InitialAssistantMessage is what the agent already said when the
	// conversation started. It is set on the first turn only.
	InitialAssistantMessage string `json:"initial_assistant_message"`
}

type sessionUpdate struct {
	Summary string `json:"summary,omitempty"`
	Status  string `json:"status,omitempty"`
}

type reply struct {
	Reply   string         `json:"reply,omitempty"`
	Session *sessionUpdate `json:"session,omitempty"`
}

func (b *Bridge) handleMessage(w http.ResponseWriter, r *http.Request) {
	var req Message
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed json"})
		return
	}
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is required"})
		return
	}

	agent, ok := b.AgentFor(req.Agent)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": fmt.Sprintf("no openclaw agent configured for agent %q", req.Agent),
		})
		return
	}

	target := agent.Model
	if target == "" {
		target = "openclaw/" + agent.ID
	}

	// An empty vireo session id means the agent is stateless: vireo mints
	// nothing. Without a `user` value the gateway mints a one-shot session per
	// request, which is exactly the wanted behaviour — no history, no carry-over.
	user := ""
	if strings.TrimSpace(req.SessionID) != "" {
		user = SessionKey(req.SessionID)
	}
	b.cfg.Logger.Printf("message agent=%q openclaw=%q session=%q user=%q start=%t",
		req.Agent, agent.ID, req.SessionID, user, req.InitialAssistantMessage != "")

	// vireo always sends Accept: application/x-ndjson, so this is the normal
	// path: a progress line keeps a slow model turn from looking like a hang.
	stream := strings.Contains(r.Header.Get("Accept"), "application/x-ndjson")
	var out *streamWriter
	if stream {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		out = &streamWriter{w: w}
		out.partial("openclaw is thinking…")
	}

	answer, err := b.complete(r.Context(), target, user, req.Message, req.InitialAssistantMessage)
	if err != nil {
		b.cfg.Logger.Printf("gateway error: %v", err)
		if stream {
			_ = out.write(map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	payload := reply{
		Reply:   answer,
		Session: &sessionUpdate{Summary: summarize(answer)},
	}
	if stream {
		_ = out.write(payload)
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

// SessionKey maps a vireo session id to the gateway's opaque session handle.
//
// Hashing is deliberate. The gateway lowercases the key it is given, so passing
// vireo's id through unchanged would merge "AbC…" and "abc…" into one
// conversation; and the value must stay clear of the reserved `subagent:`,
// `cron:` and `acp:` namespaces. A hex digest is lowercase, charset-safe and
// deterministic, so the same conversation always lands on the same gateway
// session. The `vireo-` prefix keeps the keys recognisable in the Control UI.
func SessionKey(vireoSessionID string) string {
	sum := sha256.Sum256([]byte("vireo:" + vireoSessionID))
	return "vireo-" + hex.EncodeToString(sum[:16])
}

// --- the gateway call -------------------------------------------------------

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type completionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	// User is the session handle. Omitted for a stateless turn.
	User string `json:"user,omitempty"`
}

type completionResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

func (b *Bridge) complete(ctx context.Context, model, user, message, startMessage string) (string, error) {
	// Only the new turn is sent. The gateway holds the history for this
	// session; resending it would duplicate every earlier message.
	messages := make([]chatMessage, 0, 2)
	if s := strings.TrimSpace(startMessage); s != "" {
		// The vireo client already showed this greeting to the caller, so the
		// agent must know it is on the record — otherwise it repeats itself or
		// contradicts what it "never said".
		messages = append(messages, chatMessage{
			Role:    "system",
			Content: "This conversation opened with your greeting, already delivered to the user: " + s,
		})
	}
	messages = append(messages, chatMessage{Role: "user", Content: message})

	body, err := json.Marshal(completionRequest{Model: model, Messages: messages, User: user})
	if err != nil {
		return "", fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.cfg.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.cfg.Token)

	resp, err := b.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gateway request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", fmt.Errorf("gateway read: %w", err)
	}

	var out completionResponse
	_ = json.Unmarshal(data, &out)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if out.Error != nil && out.Error.Message != "" {
			// 409 is the archived-session case: a state problem the operator
			// fixes in the Control UI, not a transient failure.
			return "", fmt.Errorf("gateway %s [%s]: %s", resp.Status, out.Error.Type, out.Error.Message)
		}
		return "", fmt.Errorf("gateway %s: %s", resp.Status, snippet(data))
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("gateway returned no choices: %s", snippet(data))
	}

	if out.Usage != nil {
		b.cfg.Logger.Printf("usage model=%s prompt=%d completion=%d total=%d finish=%s",
			out.Model, out.Usage.PromptTokens, out.Usage.CompletionTokens, out.Usage.TotalTokens,
			out.Choices[0].FinishReason)
	}

	answer := strings.TrimSpace(out.Choices[0].Message.Content)
	if answer == "" {
		return "", fmt.Errorf("gateway returned an empty answer (finish_reason=%s)", out.Choices[0].FinishReason)
	}
	return answer, nil
}

// summarize builds the one-line summary that shows in vireo's conversation
// list.
func summarize(s string) string {
	line := strings.Join(strings.Fields(s), " ")
	r := []rune(line)
	if len(r) > 90 {
		return string(r[:90]) + "…"
	}
	return line
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}

// streamWriter writes the newline-delimited lines vireo reads. Every line is
// flushed as produced, so a progress line is visible immediately.
type streamWriter struct {
	w http.ResponseWriter
}

func (s *streamWriter) write(v any) error {
	if err := json.NewEncoder(s.w).Encode(v); err != nil {
		return err
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

// partial sends one progress line: text the client shows while it waits.
func (s *streamWriter) partial(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	_ = s.write(map[string]string{"partial": text})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
