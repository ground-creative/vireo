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

// replies holds the canned answers per agent. The first line is the answer;
// the second is the session summary.
var replies = map[string][][2]string{
	"orders": {
		{"Sure — margherita, marinara and tiramisu are on the menu today. Tell me how many pizzas you want and I'll put the order in.",
			"took an order request"},
		{"Order noted. Pickup is twenty minutes from now; say the word if you want it at a table instead.",
			"confirmed an order for pickup"},
		{"I can do that, but I need a pizza and a party size before anything goes through.",
			"asked for order details"},
		{"Done — two margherita, one marinara. That is fourteen euros fifty and it will be ready shortly.",
			"placed an order"},
	},
	"reservations": {
		{"I can book you a table. What day, what time, and how many people?",
			"asked for booking details"},
		{"Table held for four people at eight o'clock. Anything else you want to add to the booking?",
			"held a table for four"},
		{"Cancelled — the booking is gone and the table is back in the pool.",
			"cancelled a booking"},
		{"We have space most evenings. Saturday nights fill up early, so tell me before you plan around it.",
			"discussed availability"},
	},
	"concierge": {
		{"Ask me anything — hours, directions, what to order. I will do my best with three canned sentences.",
			"answered a general question"},
		{"That is outside my three sentences, but the vireo daemon will happily carry the question to a backend that knows.",
			"passed on an unusual question"},
		{"I am a demo agent, so my answers are fixed. The session, the transport and the manifest are all real though.",
			"explained the demo"},
	},
}

var (
	mu     sync.Mutex
	turns  = map[string]int{}
	rng    = rand.New(rand.NewSource(time.Now().UnixNano()))
	agents = []string{"orders", "reservations", "concierge"}
)

func main() {
	addr := flag.String("addr", ":8088", "listen address")
	flag.Parse()

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		send(w, map[string]string{"status": "ok"})
	})
	http.HandleFunc("/vireo/message", handleMessage)

	fmt.Println(banner)
	log.Printf("fake business backend listening on %s (POST /vireo/message, agents: %s)", *addr, strings.Join(agents, ", "))
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
		req.Agent = r.Header.Get("X-Vireo-Agent")
	}
	n := countTurn(req.Agent + "/" + req.SessionID)
	log.Printf("webhook agent=%q session=%q turn=%d message=%q", req.Agent, req.SessionID, n, req.Message)

	pool, ok := replies[req.Agent]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		send(w, map[string]string{"error": "unknown agent " + req.Agent})
		return
	}

	mu.Lock()
	pick := pool[rng.Intn(len(pool))]
	mu.Unlock()

	send(w, reply{
		Reply:   pick[0],
		Session: &sessionUpdate{Summary: pick[1]},
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
