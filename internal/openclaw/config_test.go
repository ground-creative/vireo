package openclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfig writes a config file into a temp dir and returns its path.
func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "openclaw-bridge.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestLoadConfigDefaults covers the shipped example's shape: no token in the
// file, everything else defaulted or read from YAML.
func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv(TokenEnvVar, "from-env")
	path := writeConfig(t, `
listen: ":18090"
openclaw:
  base_url: "http://127.0.0.1:18789"
  timeout: "90s"
default: "concierge"
agents:
  concierge:
    id: "main"
    label: "Main agent"
`)
	cfg, listen, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if listen != ":18090" {
		t.Fatalf("listen = %q", listen)
	}
	if cfg.Token != "from-env" {
		t.Fatalf("token = %q, want the environment fallback", cfg.Token)
	}
	if cfg.Timeout != 90*time.Second {
		t.Fatalf("timeout = %v", cfg.Timeout)
	}
	if cfg.Model != DefaultModel {
		t.Fatalf("model = %q, want the default target", cfg.Model)
	}
	if got := cfg.Agents["concierge"]; got.ID != "main" || got.Label != "Main agent" {
		t.Fatalf("agent = %+v", got)
	}
	if cfg.Default != "concierge" {
		t.Fatalf("default = %q", cfg.Default)
	}
}

// TestLoadConfigFileTokenWins covers the file beating the environment, so a
// per-instance token is possible.
func TestLoadConfigFileTokenWins(t *testing.T) {
	t.Setenv(TokenEnvVar, "from-env")
	path := writeConfig(t, `
openclaw:
  token: "from-file"
agents:
  concierge:
    id: "main"
`)
	cfg, _, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.Token != "from-file" {
		t.Fatalf("token = %q, want the file value", cfg.Token)
	}
}

// TestLoadConfigDefaultsListenAndTimeout covers an almost-empty file: the
// port and the timeout must not be zero.
func TestLoadConfigDefaultsListenAndTimeout(t *testing.T) {
	path := writeConfig(t, `
agents:
  concierge:
    id: "main"
`)
	cfg, listen, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if listen != DefaultListen {
		t.Fatalf("listen = %q, want %q", listen, DefaultListen)
	}
	if cfg.Timeout <= 0 {
		t.Fatalf("timeout = %v, want a positive default", cfg.Timeout)
	}
	if cfg.BaseURL != DefaultGatewayURL {
		t.Fatalf("base_url = %q, want %q", cfg.BaseURL, DefaultGatewayURL)
	}
}

// TestLoadConfigUnknownKey covers the strict decoder: a typo fails loudly
// instead of being silently ignored.
func TestLoadConfigUnknownKey(t *testing.T) {
	path := writeConfig(t, `
listen: ":18090"
opencalw:
  base_url: "http://127.0.0.1:18789"
agents:
  concierge:
    id: "main"
`)
	_, _, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "opencalw") {
		t.Fatalf("err = %v, want a complaint about the misspelled key", err)
	}
}

// TestLoadConfigMissingFile covers a wrong path, the common first run.
func TestLoadConfigMissingFile(t *testing.T) {
	_, _, err := LoadConfig(filepath.Join(t.TempDir(), "nope.yaml"))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v, want a not-found complaint", err)
	}
}
