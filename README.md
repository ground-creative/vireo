# vireo

vireo is a Go daemon that gives any domain a TLS 1.3 / QUIC front door, discovered via DNS SRV (`_vireo._tcp.<domain>`), where AI assistants inspect a runtime manifest of exposed agents and converse with them over durable sessions.

```text
AI host ──MCP──▶ vireo-mcp ──[DNS SRV + vireo/1]──▶ vireo ──HTTP POST──▶ your backend
```

## What it does

Connecting an AI assistant to a service today means one of three bad options: scrape the site, write custom API glue for every assistant, or install a separate MCP server for every vendor you want to reach. None of them scale past the first integration, and none of them give you runtime discovery or multi-turn state.

vireo replaces that with a single daemon per domain, advertised via a standard DNS SRV record (`_vireo._tcp.<domain>`). When a client connects, vireo listens on one port (TLS 1.3 and QUIC), serves a runtime manifest of the domain's available agents, and routes multi-turn conversations inside durable sessions to one HTTP webhook—leaving your backend in full control of execution. One DNS lookup per domain, one bridge per AI host, and zero per-vendor client config.

## Quick Start

```bash
git clone https://github.com/ground-creative/vireo.git
cd vireo
go build -o vireo .
go build -o vireocli ./cmd/vireocli
go build -o fakeagent ./cmd/fakeagent
```

Three terminals:

```bash
./fakeagent            # 1. demo backend on :8088
./vireo --config demo  # 2. daemon, TLS + QUIC on :7777
./vireocli -id me      # 3. peer
```

`--config` is required; the daemon only reads YAML from `config/`.

```
$ ./vireocli -id me
client id: me
vireo> connect localhost
connected to localhost:7777 (well-known port)

agents:
  1) orders         Take food orders: pizzas, sides, drinks, pickup or delivery [conversation]
  2) reservations   Check availability, book a table and cancel a booking [conversation]
  3) concierge      Answer anything else: hours, directions, general questions [conversation]

vireo> 1
orders — Take food orders: pizzas, sides, drinks, pickup or delivery

no sessions yet — type: new
orders> new

session 4f1c8a2e opened
orders[4f1c8a2e]> hi, I want to order a pizza

This is not an AI. No model runs behind this endpoint — the daemon, the session
and the transport are real, the answers are not. Configure your own agents and
their webhooks in config/.
   [open · turn 1] "demo reply"
```

The bundled backend is a stub: it returns one of a few fixed lines so the wire
path can be exercised without a model. Numbers select from the current screen;
`sessions` lists what you already started; `help` lists the rest.

## Connecting an AI (`vireo-mcp`)

An AI host speaks MCP, not `vireo/1`. The bridge between them is `vireo-mcp`,
maintained in its own repository:
[`ground-creative/vireo-mcp`](https://github.com/ground-creative/vireo-mcp).

```bash
go install github.com/ground-creative/vireo-mcp@latest
```

<!-- TODO: replace the command with the released binary name/path once vireo-mcp ships -->

Register it with the host — Claude Desktop, Cursor, VS Code:

```json
{
  "mcpServers": {
    "vireo": { "command": "vireo-mcp" }
  }
}
```

The host then connects to a domain by name; the bridge resolves it, reads the
manifest and lists the agents.

<!-- TODO: no public daemon yet. Replace with the real demo hostname once deployed. -->

**Live demo:** not deployed yet. When it is, it will be reachable as
`connect demo.vireo.dev`, discovered through `_vireo._tcp.demo.vireo.dev`.

## Backend Webhook

Every turn is one HTTP POST to the agent's `transport`.

```
POST /vireo/message
Content-Type: application/json
X-Vireo-Protocol: vireo/1
X-Vireo-Agent: orders
X-Vireo-Session: 4f1c8a2e9b7d4c0fa1e2d3b4c5a6f7e8

{
  "agent": "orders",
  "session_id": "4f1c8a2e9b7d4c0fa1e2d3b4c5a6f7e8",
  "message": "I'd like 2 margherita pizzas for pickup"
}
```

Answer with any 2xx and JSON:

```json
{
  "reply": "Two margherita for pickup, ready in twenty minutes.",
  "session": { "summary": "ordered 2 margherita", "status": "open" }
}
```

`session.summary` replaces the envelope's summary when non-empty, and
`session.status: "closed"` ends the conversation after the turn. A bare JSON
string is accepted as the reply.

```go
func handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent     string `json:"agent"`
		SessionID string `json:"session_id"`
		Message   string `json:"message"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	json.NewEncoder(w).Encode(map[string]any{
		"reply":   "You said: " + req.Message,
		"session": map[string]any{"summary": "handled a message"},
	})
}
```

A non-2xx status surfaces as `upstream_error`; bodies are capped at 8 MiB; the
per-agent `timeout` (default `30s`) bounds the call.

## Configuration & DNS Discovery

```yaml
server:
  port: 7777
  cert_file: "certs/cert.pem"
  key_file: "certs/key.pem"
  endpoint:
    id: "restaurant"
    name: "restaurant"

transport: "https://your-host.example/vireo/message"

agents:
  - name: "orders"
    description: "Take food orders: pizzas, sides, drinks, pickup or delivery"
    sessions: true
```

```bash
./vireo --config restaurant    # reads config/restaurant.yaml
./scripts/gen-certs.sh         # development certificate for localhost
```

Peers resolve `_vireo._tcp.<domain>` first, then fall back to port 7777:

```
_vireo._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

## Documentation

- [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — frames, handshake, manifest, message
  and session payloads, webhook contract, error codes
- [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) — every key, defaults,
  inheritance, startup validation
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — build, Docker, certificates,
  install, firewall, DNS, systemd, operations
