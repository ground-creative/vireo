// Command vireo is a small daemon that lets AI assistants talk to business
// agents directly over the network: TLS 1.3 (TCP) and QUIC (UDP) on one port,
// ALPN portico/1, a negotiate/manifest handshake with a required client id, and
// messages forwarded to the business agents over an HTTP webhook. Sessions are
// server-minted, scoped by client id and agent.
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/quic-go/quic-go"
	"gopkg.in/yaml.v3"

	"vireo/internal/frame"
	"vireo/internal/protocol"
	"vireo/internal/server"
	"vireo/internal/session"
	"vireo/internal/upstream"
)

type config struct {
	Server    serverConf        `yaml:"server"`
	Transport string            `yaml:"transport"`
	Session   sessionConf       `yaml:"session"`
	Refs      map[string]string `yaml:"refs"`
	Links     []linkConf        `yaml:"links"`
	Agents    []agentConf       `yaml:"agents"`
}

// linkConf is one published reference. It exists because refs (a map of key to
// URL) cannot say what a link is: a client could not tell a menu PDF from an
// OpenAPI document. refs is still read and still emitted for compatibility.
type linkConf struct {
	Rel         string `yaml:"rel"`
	Href        string `yaml:"href"`
	Label       string `yaml:"label"`
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
}

// rateLimitConf is a pointer in serverConf, and Enabled is a pointer here, so
// the YAML can tell three cases apart:
//
//	no rate_limit block      -> limiting off
//	rate_limit block         -> limiting on with the defaults
//	rate_limit.enabled=false -> limiting off explicitly
//
// A plain bool cannot express the middle case: it would read as false.
type rateLimitConf struct {
	Enabled           *bool                     `yaml:"enabled"`
	RequestsPerMinute int                       `yaml:"requests_per_minute"`
	Burst             int                       `yaml:"burst"`
	Agents            map[string]AgentRateLimit `yaml:"agents"`
}

type AgentRateLimit struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	Burst             int `yaml:"burst"`
}

type serverConf struct {
	Port        int            `yaml:"port"`
	CertFile    string         `yaml:"cert_file"`
	KeyFile     string         `yaml:"key_file"`
	ALPN        []string       `yaml:"alpn"`
	CallTimeout string         `yaml:"call_timeout"`
	RateLimit   *rateLimitConf `yaml:"rate_limit"`
	Endpoint    endpointConf   `yaml:"endpoint"`
}

type endpointConf struct {
	ID          string `yaml:"id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

type mintConf struct {
	Length  int    `yaml:"length"`
	Prefix  string `yaml:"prefix"`
	Charset string `yaml:"charset"`
	Regex   string `yaml:"regex"`
}

type sessionConf struct {
	List      bool      `yaml:"list"`
	TTL       int       `yaml:"ttl_default"`
	Mint      mintConf  `yaml:",inline"`
	Store     string    `yaml:"store"` // memory | file
	File      string    `yaml:"file"`  // file name inside sessions/
	Retention string    `yaml:"retention"`
	Redis     redisConf `yaml:"redis"`
}

type redisConf struct {
	Addr   string `yaml:"addr"`
	Prefix string `yaml:"prefix"`
}

type agentSessionConf struct {
	TTL  int      `yaml:"ttl"`
	Mint mintConf `yaml:",inline"`
}

type agentConf struct {
	Name         string            `yaml:"name"`
	Description  string            `yaml:"description"`
	Tags         []string          `yaml:"tags"`
	StartMessage string            `yaml:"start_message"`
	Sessions     *bool             `yaml:"sessions"`
	Transport    string            `yaml:"transport"`
	Timeout      string            `yaml:"timeout"`
	Session      *agentSessionConf `yaml:"session"`
	Links        []linkConf        `yaml:"links"`
	Refs         map[string]string `yaml:"refs"`
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	configName := flag.String("config", "", "config name inside config/ (required, e.g. demo)")
	flag.Parse()

	if strings.TrimSpace(*configName) == "" {
		log.Fatal("config: --config is required (a YAML file inside config/, e.g. --config demo)")
	}
	configPath, err := resolveConfigPath(*configName)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	cfg, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	log.Printf("loaded %s", configPath)

	tlsCfg, err := buildTLS(&cfg.Server)
	if err != nil {
		log.Fatalf("tls: %v", err)
	}

	registry, manifest, warnings := buildRegistry(cfg)
	for _, w := range warnings {
		log.Printf("warning: %s", w)
	}
	if registry.Len() == 0 {
		log.Printf("warning: no usable agents; every message will fail with %q", protocol.CodeAgentNotFound)
	}

	store, retention, err := buildSessionStore(cfg.Session)
	if err != nil {
		log.Fatalf("session store: %v", err)
	}

	srv := server.New(server.Config{
		TLS:          tlsCfg,
		Manifest:     manifest,
		Registry:     registry,
		Sessions:     store,
		ListSessions: cfg.Session.List,
		Retention:    retention,
		CallTimeout:  callTimeout(cfg),
		RateLimit:    buildRateLimitConfig(cfg.Server.RateLimit),
		Logger:       log.Default(),
	})

	stopSweeper := startSessionSweeper(store, retention)
	defer stopSweeper()

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen tcp %s: %v", addr, err)
	}
	quicLn, err := quic.ListenAddr(addr, tlsCfg, &quic.Config{MaxIdleTimeout: 2 * time.Minute})
	if err != nil {
		tcpLn.Close()
		log.Fatalf("listen quic %s: %v", addr, err)
	}

	log.Printf("endpoint %q (%s) fronts %d agent(s); commands: %s",
		manifest.Endpoint.Name, manifest.Endpoint.ID, registry.Len(), strings.Join(manifest.Commands, ", "))

	errCh := make(chan error, 2)
	go func() { errCh <- srv.Serve(tls.NewListener(tcpLn, tlsCfg)) }()
	go func() { errCh <- srv.ServeQUIC(quicLn, quicLn.Addr().String()) }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		if err != nil {
			log.Printf("listener stopped: %v", err)
		}
	}
	srv.Close()
	log.Printf("stopped")
}

// configDir is the only directory vireo loads YAML from. Keeping every config
// in one place is what makes the repo ignore personal files (config/*) while
// committing the demo (config/demo.yaml).
const configDir = "config"

// defaultAgentTimeout is the per-agent webhook timeout when the config does
// not set one.
const defaultAgentTimeout = 30 * time.Second

// resolveConfigPath maps a config name to the file to load. "demo",
// "demo.yaml" and "config/demo.yaml" all mean the same file. Anything outside
// configDir is rejected: configs live in exactly one place.
func resolveConfigPath(name string) (string, error) {
	p := strings.TrimSpace(name)
	if p == "" {
		return "", fmt.Errorf("no config name given")
	}
	if !strings.ContainsAny(p, `/\`) {
		if filepath.Ext(p) == "" {
			p += ".yaml"
		}
		p = filepath.Join(configDir, p)
	}
	clean := filepath.Clean(p)
	if filepath.Base(filepath.Dir(clean)) != configDir {
		return "", fmt.Errorf("%q is outside %s/ — configs live in %s/ only (try --config demo)", name, configDir, configDir)
	}
	return clean, nil
}

// sessionsDir holds the runtime session envelopes. The folder is fixed so the
// repo's ignore rules stay simple; the file name inside it is configurable.
const sessionsDir = "sessions"

// DefaultSessionFile is used when session.file is not set.
const DefaultSessionFile = "vireo-sessions.json"

// resolveSessionPath turns session.file into a file inside sessions/. Session
// state is bound to that folder: nothing else is accepted.
func resolveSessionPath(cfg sessionConf) (string, error) {
	name := strings.TrimSpace(cfg.File)
	if name == "" {
		name = DefaultSessionFile
	}
	if filepath.Base(name) != name {
		return "", fmt.Errorf("session.file %q must be a bare file name inside %s/", name, sessionsDir)
	}
	return filepath.Join(sessionsDir, name), nil
}

// buildSessionStore constructs the envelope store from config and resolves the
// retention used for pruning. Defaults are intentionally durable: a restart
// keeps conversations unless "memory" is asked for explicitly.
func buildSessionStore(cfg sessionConf) (session.Store, time.Duration, error) {
	var retention time.Duration
	if cfg.Retention != "" {
		d, err := time.ParseDuration(cfg.Retention)
		if err != nil {
			return nil, 0, fmt.Errorf("retention %q is not a duration (try 720h): %w", cfg.Retention, err)
		}
		if d < 0 {
			return nil, 0, fmt.Errorf("retention %q must be positive", cfg.Retention)
		}
		retention = d
	}

	open := func() (session.Store, error) {
		path, err := resolveSessionPath(cfg)
		if err != nil {
			return nil, err
		}
		f, err := session.OpenFile(path, log.Printf)
		if err != nil {
			return nil, err
		}
		c := f.Count()
		log.Printf("session store: file %s — %d record(s) (%d open, %d closed, %d expired)",
			path, c.Total, c.Open, c.Closed, c.Expired)
		return f, nil
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Store)) {
	case "memory":
		log.Printf("session store: memory — sessions are lost on restart")
		return session.NewMemory(), retention, nil
	case "", "file":
		st, err := open()
		return st, retention, err
	default:
		log.Printf("warning: session store %q is not implemented; using the file store", cfg.Store)
		st, err := open()
		return st, retention, err
	}
}

// startSessionSweeper prunes finished sessions at startup and then in the
// background, so a long-running daemon does not accumulate history forever.
// It returns a stop function; with no retention configured it does nothing.
func startSessionSweeper(store session.Store, retention time.Duration) func() {
	if retention <= 0 {
		log.Printf("session retention: disabled — closed and expired records are kept forever")
		return func() {}
	}

	interval := retention / 10
	if interval < time.Minute {
		interval = time.Minute
	}
	if interval > time.Hour {
		interval = time.Hour
	}
	if n := store.Prune(time.Now(), retention); n > 0 {
		log.Printf("session sweep: removed %d record(s) older than %s", n, retention)
	}

	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if n := store.Prune(time.Now(), retention); n > 0 {
					c := store.Count()
					log.Printf("session sweep: removed %d record(s) older than %s (open=%d closed=%d expired=%d)",
						n, retention, c.Open, c.Closed, c.Expired)
				}
			}
		}
	}()

	var once sync.Once
	return func() { once.Do(func() { close(stop) }) }
}

func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// buildTLS loads the certificate and resolves ALPN. "h3" is dropped with a
// warning: nothing in the daemon speaks HTTP/3.
func buildTLS(c *serverConf) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if err != nil {
		// Certificates are deliberately not committed or baked into the image,
		// so a missing pair is the expected first failure of a fresh container.
		return nil, fmt.Errorf("load certificate: %w\n"+
			"no certificate pair at %q / %q. Generate one for development with "+
			"./scripts/gen-certs.sh, or mount a real pair over /app/certs in a container",
			err, c.CertFile, c.KeyFile)
	}
	protos := make([]string, 0, len(c.ALPN))
	for _, p := range c.ALPN {
		if p == "h3" {
			log.Printf("warning: dropping alpn %q (HTTP/3 is not implemented)", p)
			continue
		}
		protos = append(protos, p)
	}
	// Always serve every name the draft has used, not only what this config
	// happens to list. A client built against an earlier revision is otherwise
	// refused at the TLS layer before it can even negotiate, which turns a
	// rename into a flag day for anyone we did not redeploy in lockstep.
	for _, known := range protocol.ALPNCandidates {
		seen := false
		for _, p := range protos {
			if p == known {
				seen = true
				break
			}
		}
		if !seen {
			protos = append(protos, known)
		}
	}
	if len(protos) == 0 {
		protos = []string{protocol.Version}
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS13,
		NextProtos:   protos,
	}, nil
}

// callTimeout is the deadline the daemon puts on one agent call. Without an
// explicit server.call_timeout it is derived from the longest per-agent
// timeout plus slack, so configuring a slow agent is enough to make the call
// survive: a hardcoded cap would silently cut a model-backed agent short.
func callTimeout(cfg *config) time.Duration {
	longest := defaultAgentTimeout
	for _, a := range cfg.Agents {
		if strings.TrimSpace(a.Timeout) == "" {
			continue
		}
		d, err := time.ParseDuration(a.Timeout)
		if err == nil && d > longest {
			longest = d
		}
	}
	derived := longest + 15*time.Second
	if strings.TrimSpace(cfg.Server.CallTimeout) == "" {
		return derived
	}
	d, err := time.ParseDuration(cfg.Server.CallTimeout)
	if err != nil || d <= 0 {
		return derived
	}
	return d
}

// buildRateLimitConfig converts the YAML rate limit config to the server's
// internal type. A missing block disables limiting; a present block enables it
// unless it says `enabled: false`. Zero rates are left alone here: server.New
// turns them into the 60/min, burst 10 defaults.
func buildRateLimitConfig(rl *rateLimitConf) server.RateLimitConfig {
	if rl == nil {
		return server.RateLimitConfig{Enabled: false}
	}
	if rl.Enabled != nil && !*rl.Enabled {
		return server.RateLimitConfig{Enabled: false}
	}
	agentOverrides := make(map[string]server.AgentRateLimit, len(rl.Agents))
	for name, override := range rl.Agents {
		agentOverrides[name] = server.AgentRateLimit{
			RequestsPerMinute: override.RequestsPerMinute,
			Burst:             override.Burst,
		}
	}
	return server.RateLimitConfig{
		Enabled:           true,
		RequestsPerMinute: rl.RequestsPerMinute,
		Burst:             rl.Burst,
		AgentOverrides:    agentOverrides,
	}
}

// buildRegistry turns agent config into clients and resolves inheritance
// (transport, sessions, ttl, mint rules), collecting warnings instead of
// failing startup for one bad entry. It also assembles the manifest.
func buildRegistry(cfg *config) (*upstream.Registry, protocol.Manifest, []string) {
	registry := upstream.NewRegistry()
	var warnings []string

	globalTTL := time.Duration(cfg.Session.TTL) * time.Second
	if globalTTL <= 0 {
		globalTTL = time.Hour
	}

	endpoint := protocol.Endpoint{ID: cfg.Server.Endpoint.ID, Name: cfg.Server.Endpoint.Name, Description: cfg.Server.Endpoint.Description}
	agents := make([]protocol.AgentEntry, 0, len(cfg.Agents))

	for i, a := range cfg.Agents {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			warnings = append(warnings, fmt.Sprintf("agents[%d]: name is required", i))
			continue
		}
		transport := strings.TrimSpace(a.Transport)
		if transport == "" {
			transport = strings.TrimSpace(cfg.Transport)
		}
		if transport == "" {
			warnings = append(warnings, fmt.Sprintf("agents[%d] %q: no transport (agent-level or top-level)", i, name))
			continue
		}

		sessions := true
		if a.Sessions != nil {
			sessions = *a.Sessions
		}

		// Resolve mint rules: per-property inheritance.
		mintCfg := cfg.Session.Mint
		ttl := globalTTL
		if a.Session != nil {
			ttl = time.Duration(a.Session.TTL) * time.Second
			if ttl <= 0 {
				ttl = globalTTL
			}
			overridden := false
			if a.Session.Mint.Length != 0 {
				mintCfg.Length = a.Session.Mint.Length
				overridden = true
			}
			if a.Session.Mint.Prefix != "" {
				mintCfg.Prefix = a.Session.Mint.Prefix
				overridden = true
			}
			if a.Session.Mint.Charset != "" {
				mintCfg.Charset = a.Session.Mint.Charset
				overridden = true
			}
			if a.Session.Mint.Regex != "" {
				mintCfg.Regex = a.Session.Mint.Regex
			} else if overridden {
				// length/prefix/charset changed without an explicit regex:
				// re-derive instead of inheriting a contradicting one.
				mintCfg.Regex = ""
			}
		}
		mint, err := session.NewMint(mintCfg.Prefix, mintCfg.Length, mintCfg.Charset, mintCfg.Regex)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("agents[%d] %q: %v", i, name, err))
			continue
		}
		// Startup test-mint: misconfigured rules fail loudly, not at runtime.
		if sessions {
			if err := mint.TestMint(); err != nil {
				warnings = append(warnings, fmt.Sprintf("agents[%d] %q: mint rules rejected: %v", i, name, err))
				continue
			}
		}

		timeout := defaultAgentTimeout
		if strings.TrimSpace(a.Timeout) != "" {
			d, err := time.ParseDuration(a.Timeout)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("agents[%d] %q: invalid timeout %q: %v", i, name, a.Timeout, err))
				continue
			}
			timeout = d
		}

		client, err := upstream.New(upstream.Spec{
			Name:         name,
			Description:  a.Description,
			StartMessage: strings.TrimSpace(a.StartMessage),
			Transport:    transport,
			Sessions:     sessions,
			Mint:         mint,
			TTL:          ttl,
			Timeout:      timeout,
		})
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("agents[%d] %q: %v", i, name, err))
			continue
		}
		if err := registry.Add(client); err != nil {
			warnings = append(warnings, fmt.Sprintf("agents[%d] %q: %v", i, name, err))
			continue
		}
		entry := protocol.AgentEntry{
			Name:         client.Name(),
			Description:  client.Description(),
			Tags:         a.Tags,
			Sessions:     sessions,
			StartMessage: client.StartMessage(),
			Links:        linksFromConf(a.Links, a.Refs),
			Limits:       manifestLimits(cfg.Server.RateLimit, name),
			Refs:         a.Refs,
		}
		// Advertise this agent's effective session TTL: a per-agent override is
		// real, and the endpoint default alone would misstate it (MAN-35).
		if sessions {
			entry.Session = &protocol.SessionPolicy{TTLSeconds: int(ttl / time.Second)}
		}
		agents = append(agents, entry)
		log.Printf("agent ready: %s (via %s, sessions=%t, mint=%s)",
			client.Name(), scheme(transport), sessions, mint.Rule())
	}

	// Endpoint display name falls back to the first agent's name.
	if endpoint.Name == "" && len(agents) > 0 {
		endpoint.Name = agents[0].Name
	}

	// Session operations are advertised individually, so that disabling history
	// browsing does not silently disable creation — the defect MAN-29 and
	// PROTO-38 exist to prevent.
	ops := &protocol.SessionOps{
		New:   true,
		Get:   true,
		List:  cfg.Session.List,
		Close: true,
		Prune: cfg.Session.List,
	}

	// The deprecated commands list keeps its 1.0 shape, but now carries only the
	// operations actually accepted (MAN-45).
	var commands []string
	if ops.New {
		commands = append(commands, protocol.SessionOpNew)
	}
	if ops.Get {
		commands = append(commands, protocol.SessionOpGet)
	}
	if ops.List {
		commands = append(commands, protocol.SessionOpList)
	}
	if ops.Close {
		commands = append(commands, protocol.SessionOpClose)
	}
	if ops.Prune {
		commands = append(commands, protocol.SessionOpPrune)
	}
	sort.Strings(commands)

	ttlSeconds := int(globalTTL / time.Second)
	manifest := protocol.Manifest{
		ManifestVersion: protocol.ManifestVersion,
		Protocol:        protocol.Version,
		Endpoint:        endpoint,
		Links:           linksFromConf(cfg.Links, cfg.Refs),
		Capabilities: &protocol.Capabilities{
			Session:        ops,
			Progress:       true,
			Cancel:         true,
			MaxTurnSeconds: int(callTimeout(cfg) / time.Second),
		},
		Limits:   manifestLimits(cfg.Server.RateLimit, ""),
		Session:  protocol.SessionPolicy{TTLSeconds: ttlSeconds, List: cfg.Session.List},
		Agents:   agents,
		Commands: commands,
		Refs:     cfg.Refs,
		// No generated_at. The manifest is produced at startup and delivered
		// inside the connection, so its freshness is the handshake itself
		// (MAN-42). Stamping the start time made the digest change on every
		// restart for an unchanged endpoint, which is exactly what MAN-54 tells
		// a publisher not to do.
		Signature: nil,
	}
	if d, err := manifest.ComputeDigest(); err == nil {
		manifest.Digest = d
	} else {
		warnings = append(warnings, "manifest digest: "+err.Error())
	}
	// Fail loudly here rather than emitting a manifest a conforming client must
	// reject (MAN-47, MAN-48).
	if err := manifest.Validate(); err != nil {
		warnings = append(warnings, "manifest does not validate: "+err.Error())
	}
	return registry, manifest, warnings
}

// linksFromConf turns the configured links into manifest links, falling back to
// the deprecated refs map when no links were written. Existing configs keep
// working, and a caller sees exactly one representation (MAN-44).
func linksFromConf(links []linkConf, refs map[string]string) []protocol.Link {
	if len(links) == 0 {
		return protocol.LinksFromRefs(refs)
	}
	out := make([]protocol.Link, 0, len(links))
	for _, l := range links {
		out = append(out, protocol.Link{
			Rel:         l.Rel,
			Href:        l.Href,
			Label:       l.Label,
			Type:        l.Type,
			Description: l.Description,
		})
	}
	return out
}

// manifestLimits builds the advertised limits for an agent from the same
// rate_limit block the server enforces, so there is one source of truth. An
// empty agent name means the endpoint-level defaults. Limiting that is off is
// not advertised at all: silence means unknown, not unlimited (MAN-33).
func manifestLimits(rl *rateLimitConf, agent string) *protocol.Limits {
	if rl == nil || (rl.Enabled != nil && !*rl.Enabled) {
		if agent != "" {
			return nil
		}
		// Even with rate limiting off, the frame ceiling is a protocol fact, not
		// a policy choice (MAN-31).
		return &protocol.Limits{MaxMessageBytes: frame.MaxPayload}
	}
	rpm, burst := rl.RequestsPerMinute, rl.Burst
	if o, ok := rl.Agents[agent]; ok && agent != "" {
		if o.RequestsPerMinute > 0 {
			rpm = o.RequestsPerMinute
		}
		if o.Burst > 0 {
			burst = o.Burst
		}
	}
	if rpm <= 0 {
		rpm = 60
	}
	if burst <= 0 {
		burst = 10
	}
	limits := &protocol.Limits{RequestsPerMinute: rpm, Burst: burst}
	if agent == "" {
		limits.MaxMessageBytes = frame.MaxPayload
	}
	return limits
}

func scheme(transport string) string {
	if i := strings.Index(transport, "://"); i > 0 {
		return transport[:i]
	}
	return transport
}
