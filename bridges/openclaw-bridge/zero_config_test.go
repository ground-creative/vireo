package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadConfigNoFileIsNotAnError is the point of the whole flag design: a run
// with no config file must work, because a first test should be one command.
func TestLoadConfigNoFileIsNotAnError(t *testing.T) {
	dir := t.TempDir()
	// Run "from" the temp dir so the conventional filename is absent.
	wd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })

	t.Setenv(TokenEnvVar, "from-env")
	cfg, listen, err := LoadConfig("")
	if err != nil {
		t.Fatalf("LoadConfig with no file: %v", err)
	}
	if listen != DefaultListen {
		t.Fatalf("listen = %q, want %q", listen, DefaultListen)
	}
	if cfg.BaseURL != DefaultGatewayURL {
		t.Fatalf("base_url = %q, want the default", cfg.BaseURL)
	}
	if cfg.Model != DefaultModel {
		t.Fatalf("model = %q, want the default", cfg.Model)
	}
	if cfg.Timeout != 180*time.Second {
		t.Fatalf("timeout = %v, want the default", cfg.Timeout)
	}
	if cfg.Token != "from-env" {
		t.Fatalf("token = %q, want the environment", cfg.Token)
	}
	if len(cfg.Agents) != 0 {
		t.Fatalf("agents = %v, want none", cfg.Agents)
	}
}

// TestLoadConfigExplicitMissingFileIsAnError covers the other half: a typo in
// -config must not silently fall back to defaults.
func TestLoadConfigExplicitMissingFileIsAnError(t *testing.T) {
	_, _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil {
		t.Fatal("a missing explicit config was accepted")
	}
	if !strings.Contains(err.Error(), "-agent") {
		t.Fatalf("error does not name the flag-only path: %v", err)
	}
}

// TestAgentForSingleAgent covers the no-mapping case: every vireo agent name
// resolves to the one configured OpenClaw agent, which is what makes
// `-agent main` a complete configuration.
func TestAgentForSingleAgent(t *testing.T) {
	b := &Bridge{cfg: Config{DefaultAgent: "main", Logger: log.New(io.Discard, "", 0)}}
	for _, name := range []string{"concierge", "orders", "anything-at-all"} {
		a, ok := b.AgentFor(name)
		if !ok {
			t.Fatalf("AgentFor(%q) refused in single-agent mode", name)
		}
		if a.ID != "main" {
			t.Fatalf("AgentFor(%q).ID = %q, want main", name, a.ID)
		}
	}
}

// TestAgentForMappingWins covers a file that maps names: the map is authoritative
// and DefaultAgent must not shadow it.
func TestAgentForMappingWins(t *testing.T) {
	b := &Bridge{
		cfg: Config{
			Agents:       map[string]Agent{"concierge": {ID: "vireo"}},
			DefaultAgent: "main", // must be ignored when a map exists
			Logger:       log.New(io.Discard, "", 0),
		},
	}
	a, ok := b.AgentFor("concierge")
	if !ok || a.ID != "vireo" {
		t.Fatalf("AgentFor(concierge) = %+v, %v; want vireo", a, ok)
	}
	if _, ok := b.AgentFor("unknown"); ok {
		t.Fatal("unknown agent accepted despite a mapping being configured")
	}
}

// TestNewRequiresAgentOrMapping covers the one thing that cannot be guessed.
func TestNewRequiresAgentOrMapping(t *testing.T) {
	_, err := New(Config{Token: "t"})
	if err == nil {
		t.Fatal("New accepted a config with no agent and no mapping")
	}
	if !strings.Contains(err.Error(), "-agent") {
		t.Fatalf("error does not name the flag: %v", err)
	}
}

// TestAgentNamesReportsSingleAgent covers the startup line, which used to print
// an empty list in this mode.
func TestAgentNamesReportsSingleAgent(t *testing.T) {
	b := &Bridge{cfg: Config{DefaultAgent: "main"}}
	got := b.AgentNames()
	if len(got) != 1 || !strings.Contains(got[0], "main") {
		t.Fatalf("AgentNames() = %v, want one entry naming main", got)
	}
}
