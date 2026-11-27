// Command openclaw-bridge runs an OpenClaw gateway as a vireo backend.
//
// It speaks vireo's webhook contract and answers through the gateway's
// OpenAI-compatible Chat Completions endpoint:
//
//	vireo  ──webhook──▶  openclaw-bridge  ──/v1/chat/completions──▶  openclaw-gateway
//
// No config file is required. The only thing that cannot be guessed is which
// OpenClaw agent to use, and -agent supplies it:
//
//	export OPENCLAW_GATEWAY_TOKEN=...
//	./openclaw-bridge -agent main
//
// A YAML file is for what flags cannot express: several vireo agents mapped to
// different OpenClaw agents. See the README.
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
	// Flags mirror the config file, so a local run needs no file at all.
	agent := flag.String("agent", "main", "OpenClaw agent id to answer with; every vireo agent name resolves to it")
	addr := flag.String("addr", DefaultListen, "listen address")
	gateway := flag.String("gateway", "", "gateway base URL (default "+DefaultGatewayURL+")")
	model := flag.String("model", "", "model target override (default "+DefaultModel+")")
	timeout := flag.Duration("timeout", 0, "per-request ceiling toward the gateway (default 180s)")
	configPath := flag.String("config", "", "config file; optional, and only needed to map several vireo agents")
	flag.Parse()

	cfg, listen, err := LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("%v", err)
	}
	if strings.TrimSpace(*addr) != "" {
		listen = strings.TrimSpace(*addr)
	}

	// Flags fill in what an absent config file left unset. A config file that
	// maps agents wins for that mapping; the flags still override the connection
	// details, so a one-off run can point somewhere else without editing a file.
	if len(cfg.Agents) == 0 {
		cfg.DefaultAgent = strings.TrimSpace(*agent)
	}
	if *gateway != "" {
		cfg.BaseURL = strings.TrimSpace(*gateway)
	}
	if *model != "" {
		cfg.Model = strings.TrimSpace(*model)
	}
	if *timeout > 0 {
		cfg.Timeout = *timeout
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
	if path != "" {
		return "config file " + path
	}
	return "none"
}
