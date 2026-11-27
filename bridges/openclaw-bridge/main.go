// Command openclaw-bridge runs an OpenClaw gateway as a vireo backend.
//
// It is a backend in exactly the sense cmd/fakeagent is: it speaks vireo's
// webhook contract. The difference is what answers — an OpenClaw agent through
// the gateway's OpenAI-compatible Chat Completions endpoint, rather than canned
// text.
//
//	vireo  ──webhook──▶  openclaw-bridge  ──/v1/chat/completions──▶  openclaw-gateway
//
// One bridge serves as many vireo agents as you configure: each vireo agent name
// maps to its own OpenClaw agent.
//
//	openclaw-bridge.yaml      agent mapping, gateway URL, optional token
//	OPENCLAW_GATEWAY_TOKEN    the gateway token, if not in the file
//
// Sessions: vireo mints the id, the bridge hashes it into the OpenAI `user`
// value, and the gateway keeps the history. Nothing is resent between turns.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
)

func main() {
	configPath := flag.String("config", "openclaw-bridge.yaml", "bridge config file (it holds the agent mapping)")
	addr := flag.String("addr", "", "listen address, overrides the config file (default :18090)")
	flag.Parse()

	cfg, listen, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("%v", err)
	}
	if strings.TrimSpace(*addr) != "" {
		listen = strings.TrimSpace(*addr)
	}

	bridge, err := New(cfg)
	if err != nil {
		log.Fatalf("%v", err)
	}

	log.Printf("openclaw bridge listening on %s (POST /vireo/message, gateway %s, agents: %s)",
		listen, cfg.BaseURL, strings.Join(bridge.AgentNames(), ", "))
	log.Printf("token source: %s", tokenSource(*configPath))

	if err := http.ListenAndServe(listen, bridge.Handler()); err != nil {
		log.Fatal(err)
	}
}

// tokenSource says where the token came from without printing it, so a
// misconfigured bridge is diagnosable without leaking the secret.
func tokenSource(path string) string {
	if v := strings.TrimSpace(os.Getenv(TokenEnvVar)); v != "" {
		return TokenEnvVar + " (environment)"
	}
	return "config file " + path
}
