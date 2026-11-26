// Command fakeagent is the demo business backend for vireo.
//
// It answers messages over one HTTP webhook, exactly like a real backend
// would. There is no model behind it: it replies with one of a few canned
// sentences so the whole path — daemon, session, webhook, reply — can be
// exercised without an API key.
//
// The demo agents are:
//   - orders        takes food orders
//   - reservations  books and cancels tables
//   - concierge     anything else
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

type request struct {
	Agent     string `json:"agent"`
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type sessionUpdate struct {
	Summary string `json:"summary,omitempty"`
	Status  string `json:"status,omitempty"`
}

type reply struct {
	Reply   string         `json:"reply,omitempty"`
	Session *sessionUpdate `json:"session,omitempty"`
}

// banner is printed once at startup so the demo says what it is.
const banner = `fakeagent — demo backend for vireo.

This is not an AI. There is no model behind these replies: every answer is one
of a few canned sentences picked at random, so the plumbing can be exercised
without an API key. Real deployments put your own logic behind the same
webhook; vireo does not care which. Configure the agents, their transports and
their session behaviour in the YAML config under config/.`

// replies are the canned answers. Every one of them says the same thing:
// nothing intelligent is behind this endpoint. One is picked at random per
// message so a peer can see the full round trip without any model or API key.
var replies = []string{
	"This is not an AI. No model runs behind this endpoint — the daemon, the session and the transport are real, the answers are not. Configure your own agents and their webhooks in config/.",
	"You are talking to a demo backend, not a language model. Every reply here is fixed text chosen at random so the wire protocol can be exercised. Point an agent's transport at your own service to get real answers.",
	"No AI is listening: this is a small set of sentences a stub server picks from. Replace it with your own HTTP backend and vireo will route messages there instead.",
	"This endpoint is a stub. It does not read what you sent and does not try to answer it — it returns a canned line so sessions, headers and framing can be tested. Real work happens behind your own webhook.",
	"Nothing here understands you. This is the demo backend that ships with vireo: it exists to prove the pipe works, not to answer questions. Your own service goes behind the transport instead.",
}

var (
	mu    sync.Mutex
	turns = map[string]int{}
	rng   = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// known is the set of agent names this demo serves.
var known = map[string]bool{"orders": true, "reservations": true, "concierge": true}

func main() {
	addr := flag.String("addr", ":8088", "listen address")
	flag.Parse()

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]string{"status": "ok"})
	})
	http.HandleFunc("/vireo/message", handleMessage)

	fmt.Println(banner)
	log.Printf("fake business backend listening on %s (POST /vireo/message, agents: orders, reservations, concierge)", *addr)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatal(err)
	}
}

func handleMessage(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		send(w, map[string]string{"error": "malformed json"})
		return
	}
	if req.Agent == "" {
		req.Agent = r.Header.Get("X-Portico-Agent")
	}
	n := countTurn(req.Agent + "/" + req.SessionID)
	log.Printf("webhook agent=%q session=%q turn=%d message=%q", req.Agent, req.SessionID, n, req.Message)

	if _, ok := known[req.Agent]; !ok {
		w.WriteHeader(http.StatusNotFound)
		send(w, map[string]string{"error": "unknown agent " + req.Agent})
		return
	}

	// Demo the streaming path: say something first, then answer. A backend that
	// can emit progress should do exactly this when it knows a turn will be
	// slow — the client shows the line while it waits.
	if strings.Contains(r.Header.Get("Accept"), "application/x-ndjson") {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		_, _ = w.Write([]byte(`{"partial":"consulting the kitchen…"}` + "\n"))
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		line := replies[rng.Intn(len(replies))]
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reply":   line,
			"session": map[string]any{"summary": "demo reply"},
		})
		return
	}

	mu.Lock()
	line := replies[rng.Intn(len(replies))]
	mu.Unlock()

	send(w, reply{
		Reply:   line,
		Session: &sessionUpdate{Summary: "demo reply"},
	})
}

func countTurn(key string) int {
	mu.Lock()
	defer mu.Unlock()
	turns[key]++
	return turns[key]
}

func send(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode response: %v", err)
	}
}
