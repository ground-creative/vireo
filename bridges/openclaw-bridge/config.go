package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// TokenEnvVar is the environment variable that supplies the gateway token when
// the config file leaves it out. The token is a secret, so a committed config
// should never carry it.
const TokenEnvVar = "OPENCLAW_GATEWAY_TOKEN"

// DefaultListen is where the bridge listens. A high port, chosen so a local run
// does not collide with the other processes a development machine tends to have
// (8088 is fakeagent; VS Code's own server takes 8090-8094).
const DefaultListen = ":18090"

// DefaultConfigFile is read when it exists and -config was not given. Its absence
// is normal, not an error: the bridge runs on flags and defaults without it.
const DefaultConfigFile = "openclaw-bridge.yaml"

// fileConfig is the shape of openclaw-bridge.yaml. listen and the agents map
// belong to the bridge; the openclaw block holds the gateway settings.
type fileConfig struct {
	Listen   string               `yaml:"listen"`
	OpenClaw fileOpenClaw         `yaml:"openclaw"`
	Agents   map[string]fileAgent `yaml:"agents"`
	Default  string               `yaml:"default"`
}

// fileOpenClaw is the gateway block. token has an environment fallback because
// a token is a secret and does not belong in a committed file.
type fileOpenClaw struct {
	BaseURL string `yaml:"base_url"`
	Token   string `yaml:"token"`
	Model   string `yaml:"model"`
	Timeout string `yaml:"timeout"`
}

// fileAgent is one OpenClaw agent behind one vireo agent name.
type fileAgent struct {
	ID    string `yaml:"id"`
	Label string `yaml:"label"`
	Model string `yaml:"model"`
}

// LoadConfig reads the bridge config and returns it with the listen address.
//
// The file is OPTIONAL. With no file the bridge runs on defaults plus whatever
// flags set, which is the intended first experience: the only thing that cannot
// be guessed is which OpenClaw agent to use, and -agent supplies that. A file is
// for the cases flags cannot express — several vireo agents mapped to different
// OpenClaw agents.
//
// A path that was given explicitly and does not exist is still an error: a typo
// should not silently fall back to defaults.
func LoadConfig(path string) (Config, string, error) {
	cfg := Config{
		BaseURL: DefaultGatewayURL,
		Model:   DefaultModel,
		Timeout: 180 * time.Second,
	}
	listen := DefaultListen

	path = strings.TrimSpace(path)
	if path == "" {
		// No path: try the conventional name, but absence is not an error.
		if _, err := os.Stat(DefaultConfigFile); err == nil {
			path = DefaultConfigFile
		} else {
			cfg.Token = strings.TrimSpace(os.Getenv(TokenEnvVar))
			return cfg, listen, nil
		}
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, "", fmt.Errorf("config %s: not found — drop -config to run on flags and defaults, or pass -agent <openclaw-agent-id>", path)
	}
	if err != nil {
		return cfg, "", fmt.Errorf("config %s: %w", path, err)
	}
	fc, err := decodeConfig(raw)
	if err != nil {
		return cfg, "", fmt.Errorf("config %s: %w", path, err)
	}

	if fc.Listen != "" {
		listen = strings.TrimSpace(fc.Listen)
	}
	if fc.OpenClaw.BaseURL != "" {
		cfg.BaseURL = strings.TrimSpace(fc.OpenClaw.BaseURL)
	}
	if fc.OpenClaw.Model != "" {
		cfg.Model = strings.TrimSpace(fc.OpenClaw.Model)
	}
	if fc.OpenClaw.Timeout != "" {
		d, err := time.ParseDuration(strings.TrimSpace(fc.OpenClaw.Timeout))
		if err != nil {
			return cfg, "", fmt.Errorf("config %s: openclaw.timeout: %w", path, err)
		}
		cfg.Timeout = d
	}

	// The file wins; the environment is the fallback so a committed config can
	// leave the token out entirely.
	cfg.Token = strings.TrimSpace(fc.OpenClaw.Token)
	if cfg.Token == "" {
		cfg.Token = strings.TrimSpace(os.Getenv(TokenEnvVar))
	}

	cfg.Default = strings.TrimSpace(fc.Default)
	if len(fc.Agents) > 0 {
		cfg.Agents = make(map[string]Agent, len(fc.Agents))
		for name, a := range fc.Agents {
			cfg.Agents[name] = Agent{ID: a.ID, Label: a.Label, Model: a.Model}
		}
	}
	return cfg, listen, nil
}

// decodeConfig parses the file strictly, so a typo in a key fails at startup
// instead of being ignored.
func decodeConfig(raw []byte) (fileConfig, error) {
	var fc fileConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&fc); err != nil && !errors.Is(err, io.EOF) {
		return fc, err
	}
	return fc, nil
}
