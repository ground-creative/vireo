# Vireo

**An open protocol for direct communication between AI agents and any party that
controls a domain — with a working reference implementation in Go.**

Every party that owns a domain is already discoverable through it. Vireo starts
from the question of why an AI agent should not discover that party _itself_ the
same way: DNS-based discovery, a manifest of what it chooses to expose, and a
session to hold the conversation in. A business is the commonest case and the one
the examples here use; a person running an agent on their own hardware is the same
case with a different owner, and the protocol does not distinguish them.

The goal is a protocol anyone can implement. A business runs one small daemon on
its own infrastructure; any client that speaks `portico/1` and looks the domain up
gets the list of agents that business exposes and can talk to them directly. The
answers still come from the business — the daemon routes each turn to its webhook
and keeps the session. Nothing else. MCP-speaking hosts reach a daemon through the
optional `vireo-mcp` bridge; other runtimes can implement `portico/1` natively.

```text
AI host ──MCP──▶ vireo-mcp ──[DNS SRV + portico/1]──▶ vireo ──HTTP POST──▶ your backend
```

Three things sit side by side here, and keeping them apart is the point of this
README:

|                      | What it is                                                                              | Where                                                                             |
| -------------------- | --------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------- |
| **Agent Front Door** | The open specification: the problem, the protocol, the manifest, the conformance ladder | [agent-front-door-spec](https://github.com/ground-creative/agent-front-door-spec) |
| **`portico/1`**      | The concrete wire protocol implemented here today                                       | [`docs/PROTOCOL.md`](docs/PROTOCOL.md)                                            |
| **Vireo**            | The Go daemon and clients that demonstrate the architecture                             | this repository                                                                   |

Vireo is a starting point for that effort, not the owner of the proposed standard.
This daemon is one working implementation of the specification, and the protocol
it speaks is not a ratified one.

## The problem

Connecting an AI assistant to a service today usually means one of three
service-specific approaches: scrape the site, write custom API glue for every
assistant, or run a separate MCP server for every vendor you want to reach. Each
can work in isolation, and a custom API or an MCP server can implement discovery
and multi-turn sessions as well as anything else — if someone builds it for that
service. What does not exist is a common discovery and communication contract, so
each integration is its own implementation and none of it carries over to the next
business or the next assistant.

Each option has a price, and scraping has the highest. It pays in tokens: a page
of HTML is mostly navigation, script and styling, and one answer often needs
several pages read. Then the site pushes back — bot walls, captchas and
fingerprinting exist to stop exactly this, so the agent ends up impersonating a
human browser to read a page the business published publicly. When the layout
changes, the integration breaks quietly. And in that path the business has no say
in the automated access to pages it published publicly: no idea which agent is
asking, no control over what it sees, no way to answer differently.

vireo is what an assistant chooses instead of scraping when the business exposes
a compatible endpoint. Discovery is DNS-based — an SRV lookup for the domain, then
resolution of the target host — and from there the client reads what the business
offers and starts talking. The business stays in control: it decides which agents
exist and what answers them.

## The idea

A domain is already a discovery mechanism. It names the business, and the business
registers and maintains it, typically managing the DNS records through a provider.
That is a different position from a listing in a directory, a marketplace or an
AI vendor's catalogue. What if the domain also answered the question "what agents
does this business expose, and how do I talk to them?"

In the model proposed here, an agent would:

1. **Discover** the business endpoint through DNS.
2. **Retrieve** a manifest of the capabilities the business chooses to expose.
3. **Establish** a session.
4. **Communicate** through a shared protocol.
5. **Receive** explicit responses, progress and execution outcomes.

The business keeps everything that makes it a business: its backend, its agents,
its models, its business logic, its data and its infrastructure. The protocol
standardizes the contract between the two sides, not the software running behind
it.

## Proposed architecture

The protocol we are working towards would define:

1. **Discovery** — DNS-based discovery of the service associated with a business
   domain.
2. **Connection** — an authenticated connection to the discovered endpoint.
3. **Manifest** — the agents, capabilities and published references the business
   makes available.
4. **Sessions** — establishing and maintaining a communication session between an
   agent and a service.
5. **Execution** — requests, responses, progress, and explicit completion or
   failure signals.
6. **Security** — identity, authentication, authorization and capability
   boundaries.

No separate application-level agent directory or centralized discovery service is
required: DNS carries discovery, and the manifest carries the rest. No particular
AI model or agent framework needs to be involved on either side.

The current `portico/1` protocol implements a concrete subset of this — framing,
handshake, manifest, sessions, messages, a webhook contract, progress and
cancellation — and is specified frame by frame in
[`docs/PROTOCOL.md`](docs/PROTOCOL.md). The manifest object it serves is
specified separately and transport-independently in the
[Agent Front Door specification](https://github.com/ground-creative/agent-front-door-spec/blob/main/normative/MANIFEST.md);
that document, not this README, is the contract a client should implement.

## The open protocol initiative

The architecture is the easy part to describe and the hard part to agree on. The
initiative is the work of turning it into a standard that independent developers
can implement without depending on a particular vendor, programming language or
agent framework. Its value comes from interoperability, and interoperability only
happens if the rules are built in the open, by more than the people who wrote the
first implementation.

The deeper reasoning, the protocol principles, the open questions and the
invitation to take part are in the specification:
[`informative/PROPOSAL.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/informative/PROPOSAL.md).
Nothing here claims that the specification, or the protocol it defines, has already
been adopted as an independent community standard; it is the starting point we can
experiment with today.

## Vireo as the reference implementation

### What Vireo is

A single Go binary per domain. It listens on one port with TLS 1.3 and QUIC, is
found through a DNS SRV record at `_portico._tcp.<domain>`, and serves the
manifest: the agents that domain exposes,
what each one claims it can do, and the references it publishes alongside them —
a site, docs, a wiki, a status page. The manifest is built from configuration at
startup, not on every connection. Every turn then goes to one HTTP webhook. No
model, no API key, no per-vendor client code.

It was built by asking an AI to design the protocol it would use itself.

### What Vireo does — and what it does not do

It does:

- serve one port carrying both TCP + TLS 1.3 and UDP + QUIC, with ALPN selecting
  `portico/1`;
- answer discovery for a domain through a `_portico._tcp` SRV record, with no
  assumed port, and still honour the retired `_vireo._tcp` and `_frontdoor._tcp`
  records so an unmigrated peer keeps working;
- serve a manifest of agents and published links, plus the session operations it
  accepts, as the first frame of every session;
- hold session envelopes and forward every turn to one HTTP webhook;
- relay progress lines and cancellation between the caller and that webhook.

It does not:

- run a model, hold an API key, or contain business logic — whatever answers
  behind the webhook is the business's own choice;
- answer on the business's behalf; the daemon routes, the backend replies;
- store message content: session records are envelopes only — dates, summary,
  turn count;
- keep a registry. There is no separate application-level agent directory;
  discovery is DNS, and the manifest is served per endpoint;
- terminate HTTP. It speaks raw TLS and QUIC on its own port, so an nginx or
  Caddy reverse proxy cannot sit in front of it;
- require MCP. The MCP bridge is one adapter for hosts that speak MCP, not part
  of the protocol.

## Complete architecture: the two repositories

The current Vireo reference implementation spans two repositories. `vireo` — the
business-side daemon, its direct terminal client `vireo-chat`, and the
`fakeagent` test backend — carries the protocol implementation and belongs on the
business side. `vireo-mcp` is the optional AI-host-side adapter that lets an
MCP-speaking host reach a daemon.

Together they are one implementation: a daemon and an adapter for the same
protocol. They are not two independent implementations of it. Independent
implementations are what the community has yet to build, and what the initiative
is for.

```text
        AI host  (Claude Desktop · Cursor · VS Code · any MCP host)
           │
           │ MCP — stdio or Streamable HTTP
           ▼
      ┌───────────┐
      │ vireo-mcp │   AI-host side · separate repository
      └─────┬─────┘
            │ portico/1 over TLS 1.3 (TCP) or QUIC (UDP)
            │ endpoint found by DNS SRV: _portico._tcp.<domain>
            ▼
      ┌───────────┐
      │   vireo   │   business side · this repository
      │  daemon   │   handshake → manifest → session → message
      └─────┬─────┘
            │ HTTP POST (JSON; optional NDJSON progress)
            ▼
      ┌───────────┐
      │  backend  │   your model, your logic, your data
      └───────────┘

  No MCP? vireo-chat speaks portico/1 to the daemon directly:

  vireo-chat ──portico/1 (TLS/QUIC, DNS SRV)──▶ vireo ──HTTP POST──▶ backend
```

| Component         | Repository                                                                | Runs on                       | Required?                  |
| ----------------- | ------------------------------------------------------------------------- | ----------------------------- | -------------------------- |
| `vireo`           | [ground-creative/vireo](https://github.com/ground-creative/vireo)         | the business's infrastructure | yes — it is the endpoint   |
| `vireo-chat`      | [ground-creative/vireo](https://github.com/ground-creative/vireo)         | any operator's terminal       | optional — a direct client |
| `vireo-mcp`       | [ground-creative/vireo-mcp](https://github.com/ground-creative/vireo-mcp) | the AI host                   | optional — an MCP adapter  |

The backends that ship with `vireo`, and what a backend is, are in
[`docs/BACKENDS.md`](docs/BACKENDS.md) — they are interchangeable and this table is
not the place to keep counting them.

What follows from that split:

- **The wire protocol is independent of MCP.** `portico/1` is the contract between
  a client and a daemon; MCP is only how an MCP-speaking host reaches it.
- **MCP is one integration mechanism, not a requirement.** A runtime that can
  speak `portico/1` natively does not need `vireo-mcp` at all, and `vireo-chat` is
  the proof: it connects to a daemon with no MCP anywhere in the path.
- **The daemon needs no model, AI provider or API key.** It forwards a turn and
  returns an answer; whether a model, a database or a person produced that
  answer is invisible to it.
- **The business controls the exposed agents.** It decides which agents exist,
  what they say they do, and which backend answers each one.

## Why DNS SRV, not a well-known URL

Well-known URI discovery and DNS SRV discovery answer different questions, and
the difference is worth being precise about.

A well-known path provides a _predictable location_ for retrieving an agent
description. Given a domain, a client knows exactly which URL to fetch. The
document it retrieves is authoritative about the rest: it can advertise interface
URLs that point at a different host or port, as A2A's Agent Card does. That is a
document-retrieval convention, and it works well when a document is what you
need.

An SRV record provides _DNS-level service endpoint discovery_. It names the target
host and port for a service at that domain, before any HTTP request exists. That
matters when the agent endpoint is not the website — another host, another port,
another protocol is a normal deployment, and SRV expresses it as a routing fact
rather than as a pointer discovered after fetching a document. SIP (RFC 3263),
XMPP, LDAP and Matrix use SRV for exactly this reason.

The layers stay separate either way: DNS routes, TLS protects the connection, and
the manifest arrives inside that connection — so the certificate at the SRV target
has to cover the queried domain, which is the name the client sends as
`ServerName`. TLS encrypts and integrity-protects the conversation with whichever
peer completed the handshake; certificate-chain and hostname verification are what
identify that peer as the intended endpoint, and what makes impersonation by an
active attacker fail. That verification is not the default in the current clients
(see
[Certificate verification in the clients](#certificate-verification-in-the-clients)),
so until it is enabled the manifest is protected in transit but its origin is not
established.

## What a session looks like

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

## Live demo

[ziwa.ai](https://ziwa.ai) is a real endpoint, already running — nothing to
install on the business side. With a client built in the next section, one
command:

```bash
./vireo-chat connect ziwa.ai
```

`connect ziwa.ai` resolves through `_portico._tcp.ziwa.ai`, lists what that
endpoint publishes today, and opens a session. From an AI host, the same host name
through [vireo-mcp](#connecting-ai-hosts-through-vireo-mcp).

## Install

One command, and it stays one command however many backends exist:

```bash
git clone https://github.com/ground-creative/vireo.git
cd vireo
./scripts/build.sh              # every binary → ./bin
./scripts/gen-certs.sh          # development certificate; ignored by git
```

`vireo` is the daemon and nothing else is needed to run it: no AI, no model, no
API key. It forwards each turn to whatever `transport` names.

`build.sh` discovers the binaries in `cmd/` rather than listing them, so adding a
backend never means editing a build file, this README, or the Dockerfile. What it
produces, and the one exception (the daemon is the root package), is in
[`docs/BUILDING.md`](docs/BUILDING.md).

To use a real backend instead of the stub, add
[`openclaw-bridge`](cmd/openclaw-bridge) — it has its own setup, because it needs a
gateway:

```bash
export OPENCLAW_GATEWAY_TOKEN="$(jq -r '.gateway.auth.token' ~/.openclaw/openclaw.json)"
./scripts/dev.sh --config openclaw
```

Docker builds the same set. Certificates are not in the image — mount a pair
(generated with `./scripts/gen-certs.sh`, or your own) over `/app/certs`:

```bash
docker build -t vireo .
docker run --rm -p 7777:7777/tcp -p 7777:7777/udp \
  -v "$PWD/config:/app/config:ro" \
  -v "$PWD/certs:/app/certs:ro" \
  -v "$PWD/sessions:/app/sessions" \
  vireo --config demo
```

Both protocols must be published: `7777/tcp` for TLS, `7777/udp` for QUIC.

Tagged releases with prebuilt binaries for Linux and macOS (amd64, arm64) are
intended so you can skip the Go toolchain — nothing is published yet, so build
from source.

### Check the install

One command runs the whole local stack — it generates a certificate, builds what
is stale, starts the backend, the daemon and the client, and stops everything when
you quit:

```bash
./scripts/dev.sh                    # stub backend + daemon + client
./scripts/dev.sh --config openclaw  # also starts the OpenClaw gateway and bridge
```

Then `connect localhost:7777` in the client, pick an agent, and send a message.
Every process's output is prefixed with its name, so a failure is attributable at
a glance. `--config` is required by the daemon and reads YAML only from
`config/`; `fakeagent` stands in for your own backend and disappears the moment
`transport` names a real service.

See [`docs/LOCAL-TESTING.md`](docs/LOCAL-TESTING.md) for what the script does, why
the certificate exists, and when to reach for `-ca` instead of `-insecure`.

By hand, the same three processes:

```bash
./fakeagent                       # 1. stub backend on :8088
./vireo --config demo             # 2. daemon, TLS + QUIC on :7777
./vireo-chat -insecure            # 3. client, in another terminal
```

`-insecure` is correct for a loopback test: the certificate exists because the
transport is TLS-only, not because a local socket has anything to authenticate.
Use `-ca certs/cert.pem` when the same config may later point at a real
deployment — verification stays on, and a daemon presenting any other certificate
is refused. See
[Certificate verification in the clients](#certificate-verification-in-the-clients).

### Certificate verification in the clients

Both clients (`vireo-chat` and `vireo-mcp`) **verify the server certificate by
default**. That is what binds the connection to the business being contacted:
the certificate must cover the queried domain, which the client sends as
`ServerName`. Without verification the traffic is still encrypted and
integrity-protected, but only for _whichever peer completed the handshake_ — an
active attacker on the path can present its own certificate and the client
cannot tell, so the manifest's origin is not established.

Local development uses a certificate from `scripts/gen-certs.sh` that no public
CA chains to, so the handshake fails until the client is told what to do about
it. Two options, and which one is right depends on the lifetime of the setting:

**`-insecure` for a disposable local run.** Nothing is being authenticated on a
loopback socket to a daemon you started yourself:

```bash
./vireo-chat -insecure connect localhost:7777
# warning: -insecure disables certificate verification; the endpoint's identity is not established
```

**`-ca` for a setting that might outlive the test.** It trusts one certificate
and leaves verification on:

```bash
./vireo-chat -ca certs/cert.pem connect localhost:7777
```

The MCP bridge takes the same setting, as an argument in the host's server
config:

```json
{
  "mcpServers": {
    "vireo": { "command": "vireo-mcp", "args": ["-ca", "/abs/path/to/certs/cert.pem"] }
  }
}
```

`-ca` is additive: the system roots are kept, so a public certificate still
verifies normally. What it buys is that the peer must present a certificate
signed by that file **and** covering the name you asked for — the same check as
a public deployment, with one local root instead of the system set. A wrong or
unrelated certificate is still rejected. It has to be restarted after
`gen-certs.sh` regenerates the file, since the roots are loaded at startup.

Use `-insecure` as a local convenience, not as a way to make a real endpoint
work: it turns verification off rather than solving it, so the connection no
longer establishes who
answered.

`vireo-mcp` also honours `SSL_CERT_FILE`, which Go reads as the root bundle:

```json
{
  "mcpServers": {
    "vireo": {
      "command": "vireo-mcp",
      "env": { "SSL_CERT_FILE": "/abs/path/to/certs/cert.pem" }
    }
  }
}
```

That works but replaces the system roots for the whole process rather than adding
to them; prefer `-ca` where the flag is available.

A deployment with a certificate that chains to a public CA needs no flag at all.
If one deployment needs an exception, implement custom verification or
certificate pinning rather than falling back to accepting arbitrary
certificates. The normative rules are `SEC-6` and `SEC-7` in
[`agent-front-door-spec/normative/SECURITY.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/normative/SECURITY.md).

## Configuration

```yaml
server:
  port: 7777
  cert_file: "certs/cert.pem"
  key_file: "certs/key.pem"
  call_timeout: "45s"
  rate_limit:
    enabled: true
    requests_per_minute: 60
    burst: 10
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

### Rate limiting

Optional per-client token bucket rate limiting. The block is the switch: leave
it out and limiting stays off, write it and limiting is on with the defaults,
or set `enabled: false` to turn it off while keeping the numbers around:

```yaml
server:
  rate_limit:
    enabled: true
    requests_per_minute: 60 # sustained rate per client per agent
    burst: 10 # token bucket depth
    agents: # per-agent overrides
      agent-creator:
        requests_per_minute: 10
        burst: 2
```

When a client exceeds the limit, the daemon returns error code `rate_limited`
with a retry delay. Set `enabled: false` to disable.

Per-agent overrides inherit missing fields from the global values. A field set to
`0` counts as missing, so there is no way to express "zero requests" for one
agent — lower the rate or disable limiting globally instead. Full behaviour is in
[`docs/CONFIGURATION.md#rate_limit`](docs/CONFIGURATION.md#rate_limit).

### `start_message`

An agent with a `start_message` opens every conversation with that greeting:
the client shows it as soon as the session exists, and the backend is told on
the first turn that it was already said, so a model does not repeat it.

### DNS discovery

A peer given a bare domain looks it up as `_portico._tcp.<domain>`:

```
_portico._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

That record is how a domain resolves — there is no assumed port. An endpoint
without any of the accepted records has to be addressed as `host:port`.

`_portico._tcp` is what this implementation publishes and serves. It is not an
established industry standard. The implementation serves TCP/TLS and UDP/QUIC on
the same port, and both are found through this one `_tcp` record; there is no
separate `_udp` record today. The retired `_vireo._tcp` and `_frontdoor._tcp`
names keep resolving for one deprecation window (`PROTO-48`), and a client reports
which record answered so an unmigrated deployment is visible rather than silent.

## Connecting AI hosts through Vireo MCP

An MCP-speaking AI host speaks MCP, not `portico/1`. The bridge between them is
`vireo-mcp`, maintained in its own repository:
[`ground-creative/vireo-mcp`](https://github.com/ground-creative/vireo-mcp).

It is optional. It is the shortest path for a host that already speaks MCP, and
it is where MCP-specific concerns live — host configuration, stdio versus
Streamable HTTP, persistent client identity, bearer tokens, tool descriptions,
progress notifications and cancellation mapping. A runtime that speaks `portico/1`
directly, as `vireo-chat` does, never needs it.

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

What the bridge exposes as MCP tools: `discover`, `connect`, `conversations`,
`new_conversation`, `resume_conversation`, `send_message`, `close_conversation`
and `about`. An assistant follows one path — `connect {host}` → choose an agent →
`conversations` or `new_conversation` → `send_message`.

The complete documentation for that side lives with the bridge:
[`vireo-mcp`](https://github.com/ground-creative/vireo-mcp#readme). Notable
differences the host user should know about:

- identity differs by transport — a local identity file over stdio, a bearer
  token bound to a client id over HTTP;
- progress notifications reach the host over stdio only; over HTTP one POST
  yields one response, so progress is silent;
- host cancellation becomes a `cancel` frame on a best-effort basis, and it is
  not a rollback.

## Backend webhook integration

Every turn is one HTTP POST to the agent's `transport`.

```
POST /vireo/message
Content-Type: application/json
X-Portico-Protocol: portico/1
X-Portico-Agent: orders
X-Portico-Session: 4f1c8a2e9b7d4c0fa1e2d3b4c5a6f7e8

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

## Documentation and contributing

- [`agent-front-door-spec`](https://github.com/ground-creative/agent-front-door-spec) —
  **the specification**: the manifest object, the wire protocol, conformance levels,
  security considerations, schema and fixtures. Start here if you want to
  implement something; it is written to be followed without reading this code.
- [`docs/CONFIGURATION.md`](docs/CONFIGURATION.md) — every key of this daemon,
  defaults, inheritance, startup validation, and how config maps to the manifest
- [`docs/BACKENDS.md`](docs/BACKENDS.md) — what a backend is, the webhook contract,
  and how to write one
- [`docs/BUILDING.md`](docs/BUILDING.md) — what `scripts/build.sh` produces, and why
  there is no list of binaries to maintain
- [`docs/LOCAL-TESTING.md`](docs/LOCAL-TESTING.md) — `scripts/dev.sh`, why a
  certificate exists locally, and when to use `-insecure` rather than `-ca`
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — build, Docker, certificates,
  install, firewall, DNS, systemd, operations
- [`docs/PROTOCOL.md`](docs/PROTOCOL.md) — how this daemon speaks `portico/1`
- [`cmd/openclaw-bridge`](cmd/openclaw-bridge) — a real backend, with its config
  reference; [`docs/OPENCLAW-GATEWAY.md`](docs/OPENCLAW-GATEWAY.md) — the gateway
  side, including the settings a multi-user deployment needs- [`vireo-mcp`](https://github.com/ground-creative/vireo-mcp) — the MCP bridge,
  its host setup, transports, identity and tokens

The protocol is the part that needs other people. If you are building an AI
runtime, a daemon in another language, or you run a business that wants its own
agents reachable at its own domain, the place to start is the
[specification](https://github.com/ground-creative/agent-front-door-spec).
Independent implementations that interoperate are the only real measure of whether
any of this works.
