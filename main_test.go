package main

import (
	"strings"
	"testing"
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

func boolPtr(b bool) *bool { return &b }
