# vireo

vireo is a Go daemon that gives any domain a TLS 1.3 / QUIC front door, discovered via DNS SRV (`_vireo._tcp.<domain>`), where AI assistants inspect a runtime manifest of exposed agents and converse with them over durable sessions.

```text
AI host ──MCP──▶ vireo-mcp ──[DNS SRV + vireo/1]──▶ vireo ──HTTP POST──▶ your backend
```

## What it does

Connecting an AI assistant to a service today means one of three bad options: scrape the site, write custom API glue for every assistant, or install a separate MCP server for every vendor you want to reach. None of them scale past the first integration, and none of them give you runtime discovery or multi-turn state.

vireo replaces that with a single daemon per domain, advertised via a standard DNS SRV record (`_vireo._tcp.<domain>`). When a client connects, vireo listens on one port (TLS 1.3 and QUIC), serves a runtime manifest of the domain's available agents, and routes multi-turn conversations inside durable sessions to one HTTP webhook—leaving your backend in full control of execution. One DNS lookup per domain, one bridge per AI host, and zero per-vendor client config.

## Install

Build from source (Go 1.22+):

```bash
git clone https://github.com/ground-creative/vireo.git
cd vireo
go build -o vireo .
```

That is the whole thing. `vireo` is the daemon; nothing else is required to run
it, and no AI, model or API key is involved.

Optional binaries:

```bash
go build -o vireo-chat ./cmd/vireo-chat    # chat client, for talking to a daemon by hand
go build -o fakeagent  ./cmd/fakeagent     # stub backend, used only by the demo below
```

Docker:

```bash
docker build -t vireo .
docker run --rm -p 7777:7777/tcp -p 7777:7777/udp \
  -v "$PWD/config:/app/config:ro" \
  -v "$PWD/sessions:/app/sessions" \
  vireo --config demo
```

Both protocols must be published: `7777/tcp` for TLS, `7777/udp` for QUIC.

## Demo

Optional. It runs a stub backend so you can see the wire path end to end without
writing one. `fakeagent` is not part of a deployment — a real install points
`transport` at your own service and never needs it.

```bash
./fakeagent            # 1. stub backend on :8088
./vireo --config demo  # 2. daemon, TLS + QUIC on :7777
./vireo-chat           # 3. client
```

`--config` is required; the daemon only reads YAML from `config/`. The client
generates its identity once and reuses it, so conversations survive between runs.

```
$ ./vireo-chat
client id: vireo_4f1c8a2e9b7d4c0fa1e2d3b4c5a6f7e8 (/home/you/.config/vireo/client-id)
> connect localhost:7777
connected to localhost:7777 (explicit address)

agents:
  1) orders         Take food orders: pizzas, sides, drinks, pickup or delivery [conversation]
  2) reservations   Check availability, book a table and cancel a booking [conversation]
  3) concierge      Answer anything else: hours, directions, general questions [conversation]

type a number or an agent name — help for commands
vireo> 1
orders — Take food orders: pizzas, sides, drinks, pickup or delivery

no conversations yet — type: new
orders> new

conversation 4f1c8a2e started — type a message
orders[4f1c8a2e]> hi, I want to order a pizza

This is not an AI. No model runs behind this endpoint — the daemon, the session
and the transport are real, the answers are not. Configure your own agents and
their webhooks in config/.
   [open · turn 1] "demo reply"
```

Only agents are numbered. `back` returns to the main menu, which lists your
recent conversations by their stable ids — type an id to continue one, so it
keeps working however many conversations appear around it.

## Configuration

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

Configs live in `config/`; anything outside it is refused. TLS 1.3 is required —
there is no plaintext mode. Every key, default and inheritance rule is in
[`docs/CONFIGURATION.md`](docs/CONFIGURATION.md).

### DNS discovery

A peer given a bare domain looks it up as `_vireo._tcp.<domain>`:

```
_vireo._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

That record is the only way a domain resolves — there is no assumed port. An
endpoint without a record has to be addressed as `host:port`.

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

## Documentation

- [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — frames, handshake, manifest, message
  and session payloads, webhook contract, error codes
- [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) — every key, defaults,
  inheritance, startup validation
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — build, Docker, certificates,
  install, firewall, DNS, systemd, operations
