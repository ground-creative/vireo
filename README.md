# vireo

**vireo lets an AI assistant talk to a business directly.** The business runs one
small daemon at its own domain; any AI host that looks the domain up gets the list
of agents that business exposes and can hold a real conversation with them. The
answers still come from the business — vireo routes each turn to its webhook and
keeps the session. Nothing else.

It was built by asking an AI to design the protocol it would use itself.

```text
AI host ──MCP──▶ vireo-mcp ──[DNS SRV + vireo/1]──▶ vireo ──HTTP POST──▶ your backend
```

## What it is

A single Go binary per domain. It listens on one port with TLS 1.3 and QUIC, is
found through a standard DNS SRV record (`_vireo._tcp.<domain>`), and serves a
runtime manifest: the agents that domain exposes, plus the references it publishes
alongside them — a site, docs, a wiki, a status page. Every turn then goes to one
HTTP webhook. No model, no API key, no per-vendor client code.

## What it solves

Connecting an AI assistant to a service today means one of three bad options: scrape the site, write custom API glue for every assistant, or install a separate MCP server for every vendor you want to reach. None of them scale past the first integration, and none of them give you runtime discovery or multi-turn state.

Each option has a price. Scraping pays in tokens: a page of HTML is mostly
navigation, script and styling, and one answer often needs several pages read.
Then the site pushes back — bot walls, captchas and fingerprinting exist to stop
exactly this, so the agent ends up impersonating a human browser to read a page
the business published publicly. When the layout changes, the integration breaks
quietly. And the business has no say in any of it: no idea which agent is asking,
no control over what it sees, no way to answer differently.

vireo is what an assistant chooses instead of scraping. One DNS lookup per
domain: the client discovers the endpoint, reads what it offers, and starts
talking. The business stays in control — it decides which agents exist and what
answers them.

### Why SRV, not a well-known URL

A2A publishes its Agent Card at `https://{domain}/.well-known/agent-card.json`,
which pins the endpoint to the web origin's host and port. An SRV record names
infrastructure independently of the website: another host, another port, another
protocol. SIP (RFC 3263), XMPP, LDAP and Matrix all use SRV for exactly this
reason. The layers differ: DNS only routes, TLS authenticates, and the manifest
arrives inside that authenticated connection — so the certificate at the SRV
target has to cover the queried domain, which is the name the client sends as
`ServerName`.

## Try it

Build from source (Go 1.22+):

```bash
git clone https://github.com/ground-creative/vireo.git
cd vireo
go build -o vireo .
```

That is the whole thing. `vireo` is the daemon; nothing else is required to run
it, and no AI, model or API key is involved.

Tagged releases with prebuilt binaries for Linux and macOS (amd64, arm64) are
intended so you can skip the Go toolchain — nothing is published yet, so build
from source for now.

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

An assistant asks the business a question in plain language and gets an answer —
opening hours, prices, availability, whether they cover your area. Here is the
whole flow, from a domain to a reply, against an example business:

```console
$ vireo-chat connect acme-plumbing.example
connected to agent.acme-plumbing.example:7777 (SRV record)

agents:
  1) bookings   Book a visit, change or cancel an appointment [conversation]
  2) support    Questions about service, coverage and pricing [conversation]

vireo> support
support> do you cover the north side?

Yes — everything north of the river, with same-day slots before 11am.
   [open · turn 1] "coverage north side"
```

Then keep asking: the conversation is a session, so a follow-up carries the
thread instead of starting over. Swap the agents for a restaurant, a clinic or a
booking desk and the shape is the same — the answers come from the business.

### Run it locally

A stub backend, for seeing the wire path without writing one. `fakeagent` is not
part of a deployment — a real install points `transport` at your own service and
never needs it.

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
  - name: "concierge"
    description: "Answer anything else: hours, directions, general questions"
    start_message: "Hello! How can I help you?"
    sessions: true
```

```bash
./vireo --config restaurant    # reads config/restaurant.yaml
./scripts/gen-certs.sh         # development certificate for localhost
```

Configs live in `config/`; anything outside it is refused. TLS 1.3 is required —
there is no plaintext mode. Every key, default and inheritance rule is in
[`docs/CONFIGURATION.md`](docs/CONFIGURATION.md).

An agent with a `start_message` opens every conversation with that greeting:
the client shows it as soon as the session exists, and the backend is told on
the first turn that it was already said, so a model does not repeat it.

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
export PATH="$PATH:$(go env GOPATH)/bin"
```

Register it with the host — Claude Desktop, Cursor, VS Code:

```json
{
  "mcpServers": {
    "vireo": { "command": "vireo-mcp" }
  }
}
```

If the host cannot start it, use the absolute path —
`$(go env GOPATH)/bin/vireo-mcp` — instead of the bare name. Desktop hosts often
launch with a minimal `PATH` and report `spawn vireo-mcp ENOENT`; the
[vireo-mcp README](https://github.com/ground-creative/vireo-mcp#load-it-into-your-client)
covers that case in full.

The host then connects to a domain by name; the bridge resolves it, reads the
manifest and lists the agents.

Why the two installs differ: `vireo-mcp` is a single binary with no data files,
so `go install` covers it. The daemon above is built in its own checkout because
it reads `config/` and `certs/` and writes `sessions/` beside the binary.

**Live demo:** [ziwa.ai](https://ziwa.ai). Ask the host to `connect ziwa.ai` —
it resolves through `_vireo._tcp.ziwa.ai` and lists the agents behind it.

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

## Progress inside a turn

A backend can stream while it works: one NDJSON line per update,
`{"partial":"fetching the site…"}`, forwarded at once as a `partial` frame (type
`0x04`) whose payload is `{"kind":"status","seq":N,"text":"…"}`. The client shows
it as an ephemeral line.

A partial never ends the turn and never counts as one — only `end` does. Clients
that do not know the type skip it, so this is additive, and a backend that streams
nothing behaves exactly as before.

Frames: [`docs/PROTOCOL.md#frames`](docs/PROTOCOL.md#frames) · progress details:
[`docs/PROTOCOL.md#progress-saying-hold-on`](docs/PROTOCOL.md#progress-saying-hold-on).

## Cancelling a turn

A caller that no longer wants an answer sends a `cancel` frame on the stream the
`message` was sent on. The daemon stops waiting, cancels the upstream request's
context, and answers that stream with `{"ok": false, "cancelled": true}` — no
`data` frame, and the session is not touched, so a cancelled turn is not counted.

Cancellation is **cooperative and best effort, never a rollback**: the daemon can
abort the HTTP request the backend is still working on, but cannot undo what that
backend already committed. A backend sees it as `r.Context().Done()` on the
webhook request and decides what to do about it.

Turns run concurrently, so the read loop stays free while a call is in flight —
that is what makes a cancel frame readable at all. Details and the frame layout
are in [`docs/PROTOCOL.md`](docs/PROTOCOL.md#cancelling-a-turn).

## Documentation

- [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — frames, handshake, manifest, message
  and session payloads, webhook contract, error codes
- [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) — every key, defaults,
  inheritance, startup validation
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — build, Docker, certificates,
  install, firewall, DNS, systemd, operations
