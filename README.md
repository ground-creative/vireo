# vireo

vireo is a Go daemon that lets AI assistants discover and converse with external services over a self-describing wire protocol.

Today, connecting an AI assistant to a service requires either fragile browser scraping, custom API glue, or configuring a separate local integration for every single vendor.

vireo gives any domain a single network front door (`_vireo._tcp.<domain>`). It advertises available agents in a runtime manifest and routes multi-turn conversations over durable sessions—while the business backend keeps full control of execution behind a single HTTP webhook.

AI assistants connect to any vireo daemon through one universal MCP bridge, maintained in a separate repository. A command-line client is included for development and testing.

---

## How it works

A peer opens a TLS 1.3 or QUIC connection and sends a `negotiate` frame carrying a
client id. The daemon replies with a **manifest** built from its configuration:
the exposed agents, their descriptions, and whether each supports multi-turn
sessions. Discovery happens at runtime—a peer asks what is available instead of
relying on static docs or pre-installed tool schemas.

To talk to an agent, the peer sends a `message` frame with the target agent name
and payload. The daemon resolves the agent, forwards the turn to that agent's
`transport` as one HTTP POST, and returns the reply.

Multi-turn conversations run inside a session. The daemon mints the session id
when a conversation starts and persists an envelope for it—timestamps, a summary,
turn count, and status—while conversation state stays with the backend. The peer
sends the id back on each turn, so a conversation survives dropped connections
or daemon restarts. Reserved session operations manage the lifecycle.

---

## Run the demo

Three processes, three terminals, all from this directory:

```bash
go run ./cmd/fakeagent        # 1. demo backend, HTTP webhook on :8088
go run . --config demo        # 2. the daemon, TLS + QUIC on :7777
go run ./cmd/vireocli -id me  # 3. a peer, for development and testing
```

`--config` is required: the daemon only loads YAML from `config/`, and there is
no implicit demo config.

In the CLI:

```
> connect localhost
> orders                # open an agent
> new                   # start a conversation
> hi, I want to order a pizza
```

The demo backend is not an AI — it answers with one of a few canned sentences,
so the whole path can be exercised without an API key.

One-liner check, no shell:

```bash
printf 'connect localhost\norders\nnew\nhi\nquit\n' | go run ./cmd/vireocli -id smoke
```

---

## Connect your backend

Everything `vireo` forwards is one HTTP POST to the configured `transport`:

```
POST /vireo/message
Content-Type: application/json
X-Vireo-Protocol: vireo/1
X-Vireo-Agent: orders
X-Vireo-Session: y113YjxvfPQzaE6ZWhmabl-wnxthBAFJ

{
  "agent": "orders",
  "session_id": "y113YjxvfPQzaE6ZWhmabl-wnxthBAFJ",
  "message": "I'd like 2 margherita pizzas for pickup"
}
```

Answer with any 2xx and JSON:

```json
{
  "reply": "Sure — margherita, marinara and tiramisu are on the menu today.",
  "session": { "summary": "took an order request", "status": "open" }
}
```

A bare JSON string is accepted as the reply. `session.summary` replaces the
envelope's summary when non-empty, and `session.status: "closed"` closes the
conversation after the turn.

A minimal handler:

```go
func handler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent   string `json:"agent"`
		Message string `json:"message"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	json.NewEncoder(w).Encode(map[string]any{
		"reply":   "You said: " + req.Message,
		"session": map[string]any{"summary": "handled a message"},
	})
}
```

[`cmd/fakeagent/main.go`](cmd/fakeagent/main.go) is the complete reference
implementation.

Rules:

- non-2xx → the peer sees `upstream_error`
- response bodies are capped at 8 MiB
- the per-agent `timeout` (default `30s`) bounds the call

---

## Configure it

A fresh clone has exactly one config, `config/demo.yaml`. Yours is another file
in the same directory — nothing else is read.

```bash
cp config/demo.yaml config/restaurant.yaml    # 1. create yours
go run . --config restaurant                  # 2. run it by name
```

`restaurant`, `restaurant.yaml` and `config/restaurant.yaml` are the same file;
`.yaml` is implied. Anything outside `config/` is refused:

```
config: "../elsewhere.yaml" is outside config/ — configs live in config/ only
```

Then:

```yaml
server:
  port: 7777
  cert_file: "certs/cert.pem" # real certificate in production
  key_file: "certs/key.pem"
  endpoint:
    id: "restaurant-prod"
    name: "restaurant"

transport: "https://your-host.example/vireo/message"

refs:
  menu: "https://your-host.example/menu" # advertised in the manifest
  docs: "https://your-host.example/docs"

agents:
  - name: "orders"
    description: "Take food orders: pizzas, sides, drinks, pickup or delivery"
    sessions: true # holds multi-turn conversations
    transport: "https://your-host.example/orders" # optional override
    timeout: "20s" # optional
    refs:
      menu: "https://your-host.example/menu" # optional, per agent
```

Session envelopes go to `sessions/` automatically — the directory and the file
are created on the first write, and both are gitignored.

### `server`

| Key                    | Type   | Default          | Meaning                                                           |
| ---------------------- | ------ | ---------------- | ----------------------------------------------------------------- |
| `port`                 | int    | `7777` in config | one port for TCP/TLS and UDP/QUIC; unset means the OS assigns one |
| `cert_file`            | string | —                | PEM certificate chain                                             |
| `key_file`             | string | —                | PEM private key                                                   |
| `alpn`                 | list   | `["vireo/1"]`    | negotiated protocols; `h3` is dropped                             |
| `endpoint.id`          | string | —                | stable id peers see in the manifest                               |
| `endpoint.name`        | string | first agent name | display name                                                      |
| `endpoint.description` | string | —                | one-line description                                              |

### `transport`

Default webhook for every agent. An agent may override it. An agent with no
transport anywhere is skipped with a warning.

### `refs`

A map of static URLs advertised in the manifest, at the endpoint level and per
agent. Use it for whatever a peer might want to follow: a menu, docs, a
support page.

### `session`

| Key           | Type     | Default               | Meaning                                                          |
| ------------- | -------- | --------------------- | ---------------------------------------------------------------- |
| `list`        | bool     | `false`               | advertise the `session.*` operations                             |
| `ttl_default` | int (s)  | `3600`                | lifetime; slides on every message                                |
| `length`      | int      | `32`                  | random characters per minted id                                  |
| `prefix`      | string   | `""`                  | id prefix                                                        |
| `charset`     | string   | alnum + `_-`          | characters used when minting                                     |
| `regex`       | string   | derived               | validation rule for ids                                          |
| `store`       | string   | `file`                | `file` or `memory`; anything else warns and falls back to `file` |
| `file`        | string   | `vireo-sessions.json` | file name inside `sessions/`                                     |
| `retention`   | duration | disabled              | how long finished sessions are kept, e.g. `720h`                 |

`file` survives restarts (atomic rewrite per mutation); `memory` does not.
Session state is bound to `sessions/`: `file` must be a bare name. `retention`
prunes only finished records — open sessions are never removed. Mint rules are
validated at startup, so a bad rule fails immediately.

### `agents`

| Key           | Type     | Default         | Meaning                                                                  |
| ------------- | -------- | --------------- | ------------------------------------------------------------------------ |
| `name`        | string   | —               | unique agent name                                                        |
| `description` | string   | —               | what the agent handles; this is what an AI reads to decide to talk to it |
| `sessions`    | bool     | `true`          | whether the agent holds conversations                                    |
| `transport`   | string   | top-level       | this agent's webhook                                                     |
| `timeout`     | duration | `30s`           | per-message backend timeout                                              |
| `refs`        | map      | —               | static URLs for this agent                                               |
| `session`     | block    | inherits global | per-agent `ttl` and mint overrides                                       |

```yaml
agents:
  - name: reservations
    session:
      ttl: 86400
      prefix: "bk-"
      length: 6
      charset: "0123456789"
      regex: "^bk-[0-9]{6}$"
```

Properties inherit independently; changing `length`, `prefix` or `charset`
without a `regex` re-derives the regex.

---

## Command-line peer

`cmd/vireocli` is a peer for development and testing against a local daemon —
it is not the AI integration path.

```bash
go run ./cmd/vireocli -id me localhost      # connect on start
go run ./cmd/vireocli -id me                # connect later: `connect localhost`
go run ./cmd/vireocli -proto quic -id me    # QUIC instead of TLS/TCP
```

| Flag        | Default    | Meaning                                                            |
| ----------- | ---------- | ------------------------------------------------------------------ |
| `-id`       | minted     | client id sent in `negotiate`; reuse it to keep your conversations |
| `-proto`    | `tls`      | `tls` or `quic`                                                    |
| `-insecure` | `true`     | skip certificate verification (development only)                   |
| `-name`     | `vireocli` | label sent to the daemon                                           |
| `-timeout`  | `15s`      | per-request timeout                                                |

Commands: `connect <host>`, `agents`, `<agent>`, `new`, `resume <n>`, `latest`,
`sessions`, `close`, `back`, `help`, `quit`. Anything else is sent as a message
to the open agent.

---

## Wire protocol

TLS 1.3 over TCP and QUIC over UDP share one port. The first frame on a
connection is `negotiate` carrying the client id; the daemon replies with the
manifest. A message is a framed request answered by `data` + `end`, or by
`error`. Sessions ride on their own frame so the message channel stays free of
anything but conversation.

The full specification — frame layout, type codes, manifest shape, message and
session payloads, the webhook contract, discovery, and every error code — is in
[`docs/PROTOCOL.md`](docs/PROTOCOL.md).

---

## Deployment

**Ports.** One number serves two protocols: open both `7777/tcp` and `7777/udp`.
There is no HTTP reverse proxy in front — this is raw TLS and QUIC, not HTTP.

**TLS.** TLS 1.3 is required and there is no plaintext mode. Point the config at
a real certificate:

```yaml
server:
  port: 7777
  cert_file: "/etc/letsencrypt/live/agent.example.com/fullchain.pem"
  key_file: "/etc/letsencrypt/live/agent.example.com/privkey.pem"
```

**Discovery.** Clients resolve `_vireo._tcp.<domain>` before falling back to
port 7777, so publish a record and peers find you without a hard-coded port:

```
_vireo._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

**Files.** The daemon reads only `config/<name>.yaml` and writes envelopes under
`sessions/`, which must be writable by the service user.

**Service.** A minimal unit:

```ini
[Unit]
Description=vireo
After=network-online.target

[Service]
ExecStart=/opt/vireo/vireo --config restaurant
WorkingDirectory=/opt/vireo
Restart=on-failure
User=vireo

[Install]
WantedBy=multi-user.target
```

`WorkingDirectory` matters: `config/`, `sessions/` and `certs/` resolve against
it.

---

## Development

```bash
go build ./...
go test ./...
go vet ./...
gofmt -l .
```

`cmd/fakeagent` is a complete reference backend driven by `config/demo.yaml`.
