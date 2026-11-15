package main

import (
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// A YAML entry without a name must be reported with its index, so a broken
// config can be found without counting entries by hand.
func TestBuildRegistryReportsAgentIndex(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents: []agentConf{
			{Name: "orders"},
			{Description: "this one has no name"},
			{Name: "concierge"},
		},
	}

	_, _, warnings := buildRegistry(cfg)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "agents[1]") {
		t.Errorf("warning %q does not point at agents[1]", warnings[0])
	}
	if !strings.Contains(warnings[0], "name is required") {
		t.Errorf("warning %q does not say the name is required", warnings[0])
	}
}

// The other per-agent failures carry the index too.
func TestBuildRegistryIndexesOtherFailures(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents: []agentConf{
			{Name: "orders"},
			{Name: "reservations", Timeout: "not-a-duration"},
		},
	}

	_, _, warnings := buildRegistry(cfg)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if !strings.Contains(warnings[0], "agents[1]") || !strings.Contains(warnings[0], "reservations") {
		t.Errorf("warning %q does not identify the entry", warnings[0])
	}
}

// A duplicate agent name is reported against the offending entry.
func TestBuildRegistryRejectsDuplicateNames(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents: []agentConf{
			{Name: "orders"},
			{Name: "orders"},
		},
	}

	registry, _, warnings := buildRegistry(cfg)
	if registry.Len() != 1 {
		t.Errorf("registry holds %d agents, want 1", registry.Len())
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "agents[1]") {
		t.Errorf("warnings = %v, want one pointing at agents[1]", warnings)
	}
}

// A valid config produces no warnings and one agent per entry.
func TestBuildRegistryAcceptsValidConfig(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Refs:      map[string]string{"docs": "https://example.com"},
		Agents: []agentConf{
			{Name: "orders", Description: "takes orders"},
			{Name: "concierge", Sessions: boolPtr(false)},
		},
	}

	registry, manifest, warnings := buildRegistry(cfg)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if registry.Len() != 2 {
		t.Fatalf("registry holds %d agents, want 2", registry.Len())
	}
	if manifest.Refs["docs"] != "https://example.com" {
		t.Errorf("manifest refs = %v", manifest.Refs)
	}
	if len(manifest.Agents) != 2 {
		t.Fatalf("manifest lists %d agents, want 2", len(manifest.Agents))
	}
}

// The greeting is configuration: it reaches the manifest and the agent
// client, trimmed of surrounding space.
func TestBuildRegistryCarriesTheStartMessage(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents: []agentConf{
			{Name: "concierge", StartMessage: "  Hello there  "},
			{Name: "orders"},
		},
	}

	registry, manifest, warnings := buildRegistry(cfg)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(manifest.Agents) != 2 {
		t.Fatalf("manifest lists %d agents, want 2", len(manifest.Agents))
	}
	if manifest.Agents[0].StartMessage != "Hello there" {
		t.Errorf("manifest start_message = %q, want %q", manifest.Agents[0].StartMessage, "Hello there")
	}
	if manifest.Agents[1].StartMessage != "" {
		t.Errorf("manifest start_message = %q, want it empty", manifest.Agents[1].StartMessage)
	}

	client, ok := registry.LookupAgent("concierge")
	if !ok {
		t.Fatal("concierge is not in the registry")
	}
	if got := client.StartMessage(); got != "Hello there" {
		t.Errorf("client start message = %q, want %q", got, "Hello there")
	}
}

// The call deadline follows the slowest agent, so a model-backed agent is not
// cut off by a hardcoded ceiling; an explicit server.call_timeout wins.
func TestCallTimeoutFollowsTheSlowestAgent(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents: []agentConf{
			{Name: "fast"},
			{Name: "slow", Timeout: "120s"},
		},
	}
	if got, want := callTimeout(cfg), 135*time.Second; got != want {
		t.Errorf("callTimeout = %s, want %s (slowest agent + slack)", got, want)
	}

	cfg.Server.CallTimeout = "45s"
	if got, want := callTimeout(cfg), 45*time.Second; got != want {
		t.Errorf("callTimeout = %s, want the explicit %s", got, want)
	}
}

func boolPtr(b bool) *bool { return &b }

// A missing rate_limit block means "no limiting"; a present block means
// "limiting with the defaults". A plain bool cannot tell those apart, so the
// three cases are pinned here.
func TestRateLimitBlockPresence(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		enabled bool
	}{
		{name: "block absent", yaml: "port: 7777\n", enabled: false},
		{name: "block without enabled key", yaml: "rate_limit:\n  requests_per_minute: 120\n  burst: 4\n", enabled: true},
		{name: "block enabled explicitly", yaml: "rate_limit:\n  enabled: true\n", enabled: true},
		{name: "block disabled explicitly", yaml: "rate_limit:\n  enabled: false\n", enabled: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sc serverConf
			if err := yaml.Unmarshal([]byte(tc.yaml), &sc); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := buildRateLimitConfig(sc.RateLimit); got.Enabled != tc.enabled {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tc.enabled)
			}
		})
	}
}

// Configured values reach the server untouched, so server.New can fill in the
// 60/min and burst 10 defaults only where nothing was set.
func TestRateLimitCarriesValuesAndOverrides(t *testing.T) {
	var sc serverConf
	doc := "rate_limit:\n" +
		"  requests_per_minute: 120\n" +
		"  burst: 4\n" +
		"  agents:\n" +
		"    slow:\n" +
		"      requests_per_minute: 6\n" +
		"      burst: 1\n"
	if err := yaml.Unmarshal([]byte(doc), &sc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := buildRateLimitConfig(sc.RateLimit)
	if !got.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if got.RequestsPerMinute != 120 || got.Burst != 4 {
		t.Errorf("global = %d/min burst %d, want 120/min burst 4", got.RequestsPerMinute, got.Burst)
	}
	if override := got.AgentOverrides["slow"]; override.RequestsPerMinute != 6 || override.Burst != 1 {
		t.Errorf("slow override = %d/min burst %d, want 6/min burst 1", override.RequestsPerMinute, override.Burst)
	}
}
