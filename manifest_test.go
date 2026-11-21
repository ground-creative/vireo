package main

import (
	"strings"
	"testing"

	"vireo/internal/protocol"
)

// The manifest the daemon answers a negotiate with must be one a conforming
// client can accept: the required members, the advertised capabilities, and a
// digest that matches the document (MANIFEST.md, MAN-47, MAN-40).
func TestBuildRegistryEmitsAConformingManifest(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Refs:      map[string]string{"docs": "https://example.com/docs"},
		Agents: []agentConf{
			{
				Name:        "orders",
				Description: "takes orders",
			},
		},
	}

	_, manifest, warnings := buildRegistry(cfg)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}

	if manifest.ManifestVersion != "1.2" {
		t.Errorf("manifest_version = %q, want 1.2", manifest.ManifestVersion)
	}
	if err := manifest.Validate(); err != nil {
		t.Errorf("manifest does not validate: %v", err)
	}
}

// refs keeps working: it is still emitted, and its entries also appear as
// links, so a caller never sees two different vocabularies (MAN-44).
func TestRefsAreEmittedAsLinksToo(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Refs:      map[string]string{"docs": "https://example.com/docs"},
		Agents:    []agentConf{{Name: "orders"}},
	}

	_, manifest, _ := buildRegistry(cfg)

	if manifest.Refs["docs"] != "https://example.com/docs" {
		t.Errorf("deprecated refs dropped: %v", manifest.Refs)
	}
	if len(manifest.Links) != 1 {
		t.Fatalf("links = %v, want one derived from refs", manifest.Links)
	}
	if manifest.Links[0].Rel != "docs" || manifest.Links[0].Href != "https://example.com/docs" {
		t.Errorf("link = %+v, want rel=docs pointing at the refs value", manifest.Links[0])
	}
	if manifest.Links[0].Label != "" {
		t.Errorf("label = %q, want empty: refs carry no label", manifest.Links[0].Label)
	}
}

// An explicit links block wins over refs, and refs is still emitted for older
// clients.
func TestExplicitLinksWinOverRefs(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Refs:      map[string]string{"docs": "https://old.example"},
		Links: []linkConf{
			{Rel: "docs", Href: "https://new.example/docs", Label: "Documentation", Type: "text/html"},
		},
		Agents: []agentConf{{Name: "orders"}},
	}

	_, manifest, _ := buildRegistry(cfg)

	if len(manifest.Links) != 1 || manifest.Links[0].Label != "Documentation" {
		t.Fatalf("links = %+v, want the explicit entry", manifest.Links)
	}
	if manifest.Refs["docs"] != "https://old.example" {
		t.Errorf("refs = %v, want it still emitted", manifest.Refs)
	}
}

// Disabling history browsing must not disable session creation in the
// manifest: a conversational agent without session.new is unusable (MAN-29).
func TestCapabilitiesKeepSessionCreationWhenListingIsOff(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents:    []agentConf{{Name: "orders"}},
	}
	cfg.Session.List = false

	_, manifest, warnings := buildRegistry(cfg)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if manifest.Capabilities == nil || manifest.Capabilities.Session == nil {
		t.Fatal("capabilities.session is missing")
	}
	ops := manifest.Capabilities.Session
	if !ops.New || !ops.Get || !ops.Close {
		t.Errorf("session ops = %+v, want new/get/close available", ops)
	}
	if ops.List || ops.Prune {
		t.Errorf("session ops = %+v, want list/prune off", ops)
	}
}

// The deprecated commands list must name only what the endpoint accepts. It
// used to list every operation whenever listing was on, including ones the
// server would refuse (MAN-45).
func TestCommandsListOnlyEnabledOperations(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents:    []agentConf{{Name: "orders"}},
	}
	cfg.Session.List = false

	_, manifest, _ := buildRegistry(cfg)

	has := func(op string) bool {
		for _, c := range manifest.Commands {
			if c == op {
				return true
			}
		}
		return false
	}
	if !has("session.new") {
		t.Errorf("commands = %v, want session.new even with listing off", manifest.Commands)
	}
	if has("session.list") {
		t.Errorf("commands = %v, want no session.list while listing is off", manifest.Commands)
	}
}

// Limits are derived from the block the server actually enforces, so the
// advertised number cannot drift from the number applied (MAN-33).
func TestManifestLimitsFollowTheRateLimitBlock(t *testing.T) {
	enabled := true
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Server: serverConf{RateLimit: &rateLimitConf{
			Enabled:           &enabled,
			RequestsPerMinute: 120,
			Burst:             5,
			Agents:            map[string]AgentRateLimit{"slow": {RequestsPerMinute: 6, Burst: 1}},
		}},
		Agents: []agentConf{{Name: "orders"}, {Name: "slow"}},
	}

	_, manifest, _ := buildRegistry(cfg)

	if manifest.Limits == nil {
		t.Fatal("endpoint limits missing while limiting is on")
	}
	if manifest.Limits.RequestsPerMinute != 120 || manifest.Limits.Burst != 5 {
		t.Errorf("endpoint limits = %+v, want the configured 120/5", manifest.Limits)
	}
	if manifest.Limits.MaxMessageBytes == 0 {
		t.Error("max_message_bytes is zero; the frame ceiling is a protocol fact")
	}

	for _, a := range manifest.Agents {
		if a.Name != "slow" {
			if a.Limits != nil && a.Limits.RequestsPerMinute == 6 {
				t.Errorf("agent %q inherited the override meant for slow", a.Name)
			}
			continue
		}
		if a.Limits == nil || a.Limits.RequestsPerMinute != 6 || a.Limits.Burst != 1 {
			t.Errorf("slow limits = %+v, want the per-agent override 6/1", a.Limits)
		}
	}
}

// With limiting off, no rate is advertised: silence means "unknown", not
// "unlimited" (MAN-33).
func TestNoRateIsAdvertisedWhenLimitingIsOff(t *testing.T) {
	disabled := false
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Server:    serverConf{RateLimit: &rateLimitConf{Enabled: &disabled}},
		Agents:    []agentConf{{Name: "orders"}},
	}

	_, manifest, _ := buildRegistry(cfg)

	if manifest.Limits == nil {
		t.Fatal("limits missing; the frame ceiling should still be published")
	}
	if manifest.Limits.RequestsPerMinute != 0 || manifest.Limits.Burst != 0 {
		t.Errorf("limits = %+v, want no rate advertised", manifest.Limits)
	}
	if manifest.Limits.MaxMessageBytes == 0 {
		t.Error("max_message_bytes should be published even with limiting off")
	}
}

// Each agent advertises its own effective session TTL, because the endpoint
// default would misstate an agent that overrides it (MAN-35).
func TestAgentAdvertisesItsOwnSessionTTL(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents: []agentConf{
			{Name: "orders"},
			{Name: "reservations", Session: &agentSessionConf{TTL: 86400}},
		},
	}

	_, manifest, warnings := buildRegistry(cfg)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}

	if manifest.Agents[1].Session == nil || manifest.Agents[1].Session.TTLSeconds != 86400 {
		t.Errorf("reservations session = %+v, want ttl 86400", manifest.Agents[1].Session)
	}
	if manifest.Agents[0].Session == nil || manifest.Agents[0].Session.TTLSeconds != 3600 {
		t.Errorf("orders session = %+v, want the default 3600", manifest.Agents[0].Session)
	}
}

// The digest is stable for stable state and changes when the document changes
// (MAN-40, MAN-54).
func TestDigestIsStableAndSensitive(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents:    []agentConf{{Name: "orders"}},
	}
	_, manifest, _ := buildRegistry(cfg)

	if !strings.HasPrefix(manifest.Digest, "sha256:") {
		t.Fatalf("digest = %q, want a sha256 prefix", manifest.Digest)
	}

	again, err := manifest.ComputeDigest()
	if err != nil {
		t.Fatalf("ComputeDigest: %v", err)
	}
	if again != manifest.Digest {
		t.Errorf("digest changed between calls: %q then %q", manifest.Digest, again)
	}

	changed := manifest
	changed.Agents = append([]protocol.AgentEntry(nil), manifest.Agents...)
	changed.Agents[0].Description = "different"
	other, err := changed.ComputeDigest()
	if err != nil {
		t.Fatalf("ComputeDigest: %v", err)
	}
	if other == manifest.Digest {
		t.Error("digest did not change when the document did")
	}
}

// A stateless agent has no session block: advertising a TTL for an agent that
// holds no sessions would be a lie (MAN-38).
func TestStatelessAgentHasNoSessionBlock(t *testing.T) {
	cfg := &config{
		Transport: "http://127.0.0.1:8088/vireo/message",
		Agents:    []agentConf{{Name: "status", Sessions: boolPtr(false)}},
	}

	_, manifest, _ := buildRegistry(cfg)

	if manifest.Agents[0].Session != nil {
		t.Errorf("session = %+v, want nil for a stateless agent", manifest.Agents[0].Session)
	}
}
