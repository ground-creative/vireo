# OpenClaw backend

`openclaw-bridge` makes an [OpenClaw](https://openclaw.ai) gateway available as
a vireo backend. It is the same kind of program as `fakeagent` — something that
answers `POST /vireo/message` — but the answer comes from an agent behind the
gateway instead of canned text.

```
vireo-chat ──portico/1──▶ vireo ──webhook──▶ openclaw-bridge ──/v1/chat/completions──▶ openclaw-gateway
```

Three processes. vireo does not know what OpenClaw is, and the gateway does not
know what vireo is; the bridge is the only component that knows both.

| Piece             | Speaks                                  | Knows about             |
| ----------------- | --------------------------------------- | ----------------------- |
| `vireo`           | `portico/1` (TLS/QUIC) + webhook        | agents, sessions        |
| `openclaw-bridge` | webhook in, OpenAI Chat Completions out | the mapping, the token  |
| OpenClaw gateway  | OpenAI-compatible HTTP                  | agents, its own history |

## Why a bridge at all

The two sides agree on the payload shape and disagree on sessions.

vireo's webhook is `{agent, session_id, message}` in, `{reply, session}` out,
and vireo mints `session_id` itself. OpenAI's Chat Completions has no session
field at all. OpenClaw keeps history on its side, keyed by a session key it
derives from the request's `user` string.

So the bridge's one real job is the mapping:

```
vireo session id  ──sha256──▶  "vireo-<32 hex>"  ──▶  OpenAI `user`  ──▶  gateway session key
```

Consequences worth knowing:

- **History is never resent.** The gateway holds it. Sending it back would
  duplicate every earlier turn.
- **The hash is deterministic**, so the same vireo conversation always resumes
  the same gateway session, across bridge restarts.
- **The hash is lowercase hex.** The gateway lowercases session keys, so passing
  vireo's id through unchanged would merge `AbC…` and `abc…` into one
  conversation.
- **It stays clear of reserved namespaces.** The gateway rejects session keys
  beginning `subagent:`, `cron:` or `acp:`; a hex digest cannot collide.
- **A stateless vireo agent sends no `user`**, so the gateway mints a one-shot
  session per request and nothing carries over.

## Setup

### 1. Enable the gateway's OpenAI surface

It is **disabled by default**. Enable it once:

```bash
openclaw config set gateway.http.endpoints.chatCompletions.enabled true
```

That applies without a restart. Verify:

```bash
curl -s http://127.0.0.1:18789/v1/models \
  -H "Authorization: Bearer $OPENCLAW_GATEWAY_TOKEN"
```

A `404` here means the surface is still off — which is what the bridge reports
at startup rather than failing on every message.

### 2. Point the bridge at the gateway

```bash
cp openclaw-bridge.example.yaml openclaw-bridge.yaml
export OPENCLAW_GATEWAY_TOKEN="$(jq -r '.gateway.auth.token' ~/.openclaw/openclaw.json)"
```

The token is the gateway's `gateway.auth.token`. It is **not** a provider API
key, and it is **not** put in the YAML — the file is meant to be readable, and
`openclaw-bridge.yaml` is gitignored while the example is not.

### 3. Run the three processes

```bash
# terminal 1 — the gateway
openclaw gateway run

# terminal 2 — the bridge
go build -o openclaw-bridge ./cmd/openclaw-bridge
./openclaw-bridge                      # reads openclaw-bridge.yaml

# terminal 3 — the daemon
go run . --config openclaw             # reads config/openclaw.yaml

# terminal 4 — a client
go build -o vireo-chat ./cmd/vireo-chat
./vireo-chat -ca certs/cert.pem connect localhost:7779
```

In the client: pick the agent, type `new`, and send a message.

## Configuration

### `openclaw-bridge.yaml`

| Key                 | Type     | Default                  | Meaning                                          |
| ------------------- | -------- | ------------------------ | ------------------------------------------------ |
| `listen`            | string   | `:18090`                 | address the bridge binds                         |
| `openclaw.base_url` | string   | `http://127.0.0.1:18789` | the gateway                                      |
| `openclaw.token`    | string   | —                        | optional; prefer the env var                     |
| `openclaw.model`    | string   | `openclaw/default`       | default model target                             |
| `openclaw.timeout`  | duration | `180s`                   | per-request ceiling against the gateway          |
| `agents`            | map      | —                        | vireo agent name → OpenClaw agent                |
| `default`           | string   | —                        | agent used when the payload names an unknown one |

Each `agents` entry:

| Key     | Type   | Meaning                                   |
| ------- | ------ | ----------------------------------------- |
| `id`    | string | OpenClaw agent id, required               |
| `label` | string | human name, logs only                     |
| `model` | string | target override, e.g. `openclaw/research` |

`listen` is `:18090` by default because `fakeagent` takes `:8088`, the ZIWA
bridge takes `:8089`, and VS Code's own server binds `8090-8094` on a developer
machine.

### The model target

`model` is an **agent target**, not a provider model id. `openclaw/default` is
the stable alias that survives a change of the configured default agent, so it
is what the example uses. The full set the gateway accepts:

| Target                         | Routes to                    |
| ------------------------------ | ---------------------------- |
| `openclaw`, `openclaw/default` | the configured default agent |
| `openclaw/<agentId>`           | a specific agent             |

The bridge sends `openclaw/<id>` for an agent with no `model` override, and the
override verbatim when one is set.

### `config/openclaw.yaml` (the daemon side)

```yaml
server:
  port: 7779
  call_timeout: "200s" # above the bridge's 180s
transport: "http://127.0.0.1:18090/vireo/message"

agents:
  - name: "concierge"
    description: "Ask the local OpenClaw agent anything; it keeps a conversation."
    start_message: "Hi — I'm the OpenClaw agent. What can I help with?"
    timeout: "190s"
```

`call_timeout` must exceed the bridge's `openclaw.timeout`, or vireo gives up
while the model is still working. Two layers, both generous.

## Sessions

vireo mints the session id; the bridge never does. The flow matches
`config/demo.yaml`:

1. The client negotiates, picks an agent, and lists prior conversations.
2. It resumes one or creates one with `session.new`.
3. Each message carries the id; the bridge hashes it into the gateway's `user`.
4. The gateway resumes the history it holds for that key.

`session.ttl_default` in the daemon config decides when vireo forgets a
conversation. The gateway keeps its own copy; a vireo session that expired and
is recreated mints a **new** id, which hashes to a **new** gateway session. The
old gateway history is simply no longer reachable from vireo.

### Isolation between conversations

Separate vireo sessions are separate gateway sessions — but OpenClaw has three
features that deliberately share context **across** sessions, and on a personal
install each one can leak one conversation into another. For a multi-user vireo
front door they must be off:

| Feature                                         | Default       | What it does                                                                                          | Set to isolate |
| ----------------------------------------------- | ------------- | ----------------------------------------------------------------------------------------------------- | -------------- |
| `memory.search.rememberAcrossConversations`     | on (personal) | recalls other conversations' transcripts via `memory_search`                                          | `false`        |
| `tools.sessions.visibility`                     | `all`         | `sessions_search`/`_history` reach **every** session, other users included                            | `"self"`       |
| `hooks.internal.entries.session-memory.enabled` | on            | writes conversation excerpts to a **shared** workspace `MEMORY.md`, which the `memory` source indexes | `false`        |

```bash
openclaw config set memory.search.rememberAcrossConversations false --strict-json
openclaw config set tools.sessions.visibility '"self"' --strict-json
openclaw config set hooks.internal.entries.session-memory.enabled false --strict-json
```

The first two apply without a restart; the memory one may want one.

**Better still: give vireo its own agent.** These settings are gateway-wide, and
turning them off weakens your personal agent. A dedicated agent with its own
workspace keeps the two apart without touching the personal one:

```bash
openclaw config set agents.entries.vireo '{
  "name": "vireo",
  "workspace": "/home/you/.openclaw/agents/vireo/workspace",
  "agentDir": "/home/you/.openclaw/agents/vireo/agent",
  "identity": {"name": "vireo"}
}' --strict-json
```

Then set `agents.concierge.id: "vireo"` in `openclaw-bridge.yaml`. The gateway
publishes it as `openclaw/vireo` in `/v1/models`.

To verify isolation you need a value that is unique and not guessable. Ask one
conversation to remember it, then ask a second. The second must say it does not
know:

```bash
B=http://127.0.0.1:18090/vireo/message
post() { curl -s -H 'Accept: application/x-ndjson' -d "$1" $B | tail -1; }

post '{"agent":"concierge","session_id":"A1","message":"My code is ZEBRA-4417. Reply OK."}'
post '{"agent":"concierge","session_id":"A1","message":"What is my code?"}'
post '{"agent":"concierge","session_id":"B1","message":"What is my code? Say UNKNOWN if you do not know."}'
```

The third must not contain `ZEBRA-4417`.

## What the bridge does not do

- **No streaming of the reply text.** vireo's client shows partial frames; this
  bridge sends one progress line ("openclaw is thinking…") and then the full
  answer. The gateway does stream, and a later version can forward those deltas
  as `kind: "chunk"` frames.
- **No tool passthrough.** The gateway's own tools work; vireo does not proxy a
  caller's tools to it.
- **No provider model selection by the caller.** A caller picks a vireo agent;
  which model that agent runs is gateway configuration. Letting the caller
  choose would be a `x-openclaw-model` header, and the gateway treats that as
  an operator action.
- **No multi-turn history reconstruction.** It relies on the gateway holding the
  history. If the gateway loses a session, the conversation restarts on its
  side; vireo still shows its own envelope.

## Failure modes

| Symptom at startup                                | Cause                             | Fix                                                                       |
| ------------------------------------------------- | --------------------------------- | ------------------------------------------------------------------------- |
| `... /v1/models is not served`                    | chat completions disabled         | `openclaw config set gateway.http.endpoints.chatCompletions.enabled true` |
| `gateway rejected the token`                      | token is not `gateway.auth.token` | re-export it from `~/.openclaw/openclaw.json`                             |
| `gateway unreachable at …`                        | gateway not running               | `openclaw gateway run`                                                    |
| `agent "x" has no openclaw agent id`              | `agents.x.id` missing             | add the `id`                                                              |
| `default "x" is not one of the configured agents` | `default` names a missing key     | fix the name                                                              |

At runtime:

| Symptom                                 | Meaning                                                             |
| --------------------------------------- | ------------------------------------------------------------------- |
| `gateway 404`                           | the surface was disabled again                                      |
| `gateway 401`                           | token rotated; restart the bridge                                   |
| `gateway 409 [… invalid_request_error]` | the gateway session is archived; restore it in the Control UI       |
| `gateway 503 api_error`                 | provider overload                                                   |
| `gateway returned an empty answer`      | the agent produced no text; surfaced as an error, not a blank reply |
| `agent run was cancelled`               | vireo timed out and cancelled; raise `call_timeout`                 |

## Tests

```bash
go test ./internal/openclaw/
```

The suite pins the parts that must not drift silently:

- `SessionKey` is deterministic, lowercase, and clear of reserved namespaces.
  Changing it would orphan every existing conversation.
- Turn two sends the **same** `user` as turn one, and sends **only** the new
  message — history is not resent.
- A stateless turn sends no `user`.
- The start message reaches the agent as context.
- An ndjson caller gets a progress line then the answer.
- A gateway error becomes an ndjson error line, not a hang.
- An empty answer is an error, not a blank reply.
- Every startup failure names its own fix.
