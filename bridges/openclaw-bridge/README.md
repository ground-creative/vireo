# openclaw-bridge

A vireo backend that answers through an [OpenClaw](https://openclaw.ai) gateway.

vireo forwards each message over HTTP; this program turns that into an OpenClaw
agent run and returns the answer. vireo has no idea a model is involved.

```
vireo-chat ──portico/1──▶ vireo ──webhook──▶ openclaw-bridge ──/v1/chat/completions──▶ gateway
```

It is one of two backends here — the other is `fakeagent`, which returns canned
text so the plumbing can be tested without a model.
[What a backend is](../../docs/BACKENDS.md) covers the contract and how to write
your own.

---

## Install

Needs Go 1.22+ and a running OpenClaw gateway on the same machine or reachable over
the network.

```bash
git clone https://github.com/ground-creative/vireo.git
cd vireo
./scripts/build.sh          # → bin/openclaw-bridge, plus the rest
```

That builds every binary in the repository. If you want only this one:

```bash
go build -o bin/openclaw-bridge ./bridges/openclaw-bridge
```

---

## Configure the gateway first

Two things on the gateway side, before the bridge will start.

**1. The OpenAI-compatible surface is disabled by default.**

```bash
openclaw config set gateway.http.endpoints.chatCompletions.enabled true
```

Applies without a restart. Check it:

```bash
curl -s http://127.0.0.1:18789/v1/models \
  -H "Authorization: Bearer $OPENCLAW_GATEWAY_TOKEN"
```

That should list agent targets (`openclaw`, `openclaw/default`,
`openclaw/<agentId>`). A `404` means the surface is still off.

**2. The token is the gateway's operator credential.**

```bash
export OPENCLAW_GATEWAY_TOKEN="$(jq -r '.gateway.auth.token' ~/.openclaw/openclaw.json)"
```

It is not a provider API key and not scoped — a valid token is equivalent to owner
access to the gateway. Keep it in the environment, not in a file you might commit.

---

## Run

**No config file.** The one thing that cannot be guessed is which OpenClaw agent
to use, and that is a flag:

```bash
export OPENCLAW_GATEWAY_TOKEN="$(jq -r '.gateway.auth.token' ~/.openclaw/openclaw.json)"
./bin/openclaw-bridge -agent main
```

```
openclaw bridge listening on :18090 (POST /vireo/message, gateway http://127.0.0.1:18789, agents: any → main)
token source: OPENCLAW_GATEWAY_TOKEN (environment)
```

`any → main` is the whole configuration: every vireo agent name resolves to that
one OpenClaw agent. For a local test this is all you need.

### Flags

| Flag       | Default                  | Meaning                                            |
| ---------- | ------------------------ | -------------------------------------------------- |
| `-agent`   | `main`                   | OpenClaw agent id to answer with                   |
| `-addr`    | `:18090`                 | listen address                                     |
| `-gateway` | `http://127.0.0.1:18789` | gateway base URL                                   |
| `-model`   | `openclaw/default`       | model target override                              |
| `-timeout` | `180s`                   | per-request ceiling toward the gateway             |
| `-config`  | —                        | optional config file, for multi-agent mapping only |

### When you need a file

Only for what flags cannot express: **several vireo agent names mapped to
different OpenClaw agents**. Copy the example, which lives outside the source
tree because it is a deployment artifact, not code:

```bash
cp examples/openclaw-bridge.yaml openclaw-bridge.yaml   # from the repo root
./bin/openclaw-bridge            # picks up ./openclaw-bridge.yaml when present
```

`config.go` reads it from the working directory only; there is no search path and
no implicit copy step.

### Wiring vireo to it

```yaml
# config/mine.yaml
transport: "http://127.0.0.1:18090/vireo/message"
agents:
  - name: "concierge"
    description: "Ask the local OpenClaw agent anything"
```

```bash
./bin/vireo --config mine
./bin/vireo-chat -insecure connect localhost:7777
```

The agent `name` in that vireo config is the `agent` field the bridge receives.
With `-agent` (no mapping) it is ignored — every name maps to the same OpenClaw
agent. With a mapping file it must match a key.

### The one-command way

```bash
./scripts/dev.sh --backend openclaw
```

Starts the gateway if it is not already up, the bridge (flags only), the daemon,
and the client, and stops everything on exit. `OPENCLAW_AGENT` overrides the agent.

Use this for a local test. Use the explicit sequence above when wiring a real
deployment, where the vireo config is yours to write.

### If the gateway is already running

The bridge reuses it and leaves it alone on exit. `dev.sh` does the same — it only
starts a gateway it does not find.

---

## Docker

**Yes, and in the same image as the daemon.** The image contains every binary in
the repository, so the bridge is already there — only the entrypoint differs:

```bash
docker build -t vireo .

# No config file: the agent is a flag.
docker run --rm --network host \
  -e OPENCLAW_GATEWAY_TOKEN \
  --entrypoint /app/openclaw-bridge \
  vireo -agent main
```

**`--network host` is not optional.** The OpenClaw gateway binds `127.0.0.1:18789`
by default. A container on the default bridge network cannot reach the host's
loopback, and `host.docker.internal` does **not** help — it resolves to the host's
bridge address (`172.17.0.1`), which the gateway is not listening on:

```
openclaw: gateway unreachable at http://host.docker.internal:18789:
  dial tcp 172.17.0.1:18789: connect: connection refused
```

Options, in order of preference:

1. **`--network host`** — the container shares the host's loopback, so
   `base_url: http://127.0.0.1:18789` just works. What the command above uses.
2. **`--add-host=host.docker.internal:host-gateway`** plus moving the gateway off
   loopback (`gateway.bind = "lan"`). Works, but exposes the gateway on the
   network — and a valid token is full operator access, so only do it deliberately.
3. **The gateway in a container too**, on a shared user-defined network, with
   `base_url: http://<gateway-container>:18789`. The usual production shape.

Same for vireo: if `vireo` runs in a container and the bridge on the host, the
daemon's `transport` must name an address reachable from inside the container —
`127.0.0.1` there is the container itself, not the host.

### One image, or three?

One image is enough and is what this repository builds. Three containers from it,
each with its own `--entrypoint`:

```bash
docker run --entrypoint /app/openclaw-bridge vireo ...   # the bridge
docker run --entrypoint /app/vireo           vireo ...   # the daemon
docker run --entrypoint /app/fakeagent       vireo ...   # the stub, if wanted
```

They communicate by container name on a user-defined network. Building a separate
small image per binary is possible — each is a static Go binary — but you then
maintain a build per backend, which is what `scripts/build.sh` exists to avoid.

---

## Config reference

**Optional.** Only read when `-config` names a file, or when
`openclaw-bridge.yaml` exists in the working directory. Nothing here is required
for a single-agent run — the flags cover that. Unknown keys are **rejected**, so a
typo fails at startup rather than being silently ignored.

```yaml
listen: ":18090"

openclaw:
  base_url: "http://127.0.0.1:18789"
  model: "openclaw/default"
  timeout: "180s"

default: "concierge"

agents:
  concierge:
    id: "main"
```

### Top level

| Key       | Type   | Default      | Meaning                                                                                             |
| --------- | ------ | ------------ | --------------------------------------------------------------------------------------------------- |
| `listen`  | string | `:18090`     | address to bind                                                                                     |
| `default` | string | —            | agent used when the webhook names one that is not in `agents`; empty means unknown agents get a 404 |
| `agents`  | map    | **required** | vireo agent name → OpenClaw agent                                                                   |

### `openclaw`

| Key        | Type     | Default                  | Meaning                                                                             |
| ---------- | -------- | ------------------------ | ----------------------------------------------------------------------------------- |
| `base_url` | string   | `http://127.0.0.1:18789` | the gateway's HTTP API (same port as its Control UI)                                |
| `token`    | string   | —                        | optional; `OPENCLAW_GATEWAY_TOKEN` is preferred. If both are set the **file wins**  |
| `model`    | string   | `openclaw/default`       | default agent target for agents without an override                                 |
| `timeout`  | duration | `180s`                   | ceiling per request toward the gateway; vireo's `call_timeout` must be **above** it |

### `agents.<name>`

The key is the `agent` field of the webhook payload, so it must match a `name` in
your vireo config.

| Key     | Type   | Meaning                                   |
| ------- | ------ | ----------------------------------------- |
| `id`    | string | OpenClaw agent id, **required**           |
| `label` | string | human name, logs only                     |
| `model` | string | target override, e.g. `openclaw/research` |

One bridge serves several vireo agents. Each can point at a different OpenClaw
agent, or at the same one on a different model:

```yaml
agents:
  concierge:
    id: "main"
  researcher:
    id: "main"
    model: "openclaw/research"
```

### Why the port is 18090

A high port, chosen so a local run does not collide with the other processes a
development machine tends to have — 8088 is `fakeagent`, and VS Code's own server
takes 8090-8094. Any free port works: set `listen` and point vireo's `transport`
at it.

### Why the token is not in the file

The file is meant to be copied and read. `openclaw.token` exists for a throwaway
local setup; the environment variable is the intended path.

---

## Sessions

vireo mints the session id; this program never does. OpenAI's Chat Completions has
no session field, so the id is hashed into the `user` value and OpenClaw derives its
session key from that:

```
vireo session id ──sha256──▶ "vireo-<32 hex>" ──▶ OpenAI `user` ──▶ gateway session
```

- **History is never resent.** The gateway holds it; resending would duplicate
  every earlier turn.
- **Deterministic**, so a bridge restart resumes the same conversations.
- **Lowercase hex**, because the gateway lowercases session keys — two ids
  differing only by case would otherwise collapse into one conversation.
- **Clear of reserved namespaces** (`subagent:`, `cron:`, `acp:`), which the
  gateway rejects.
- **A stateless vireo agent sends no `user`**, so the gateway mints a one-shot
  session and nothing carries over.

**Isolation is a gateway-side concern, and it is off by default.** OpenClaw shares
conversation content across an agent's sessions: the `session-memory` hook writes
into a shared workspace, and the filesystem and memory tools let the model read it
back. On a default install one caller can recall what another said — measured, not
assumed, with the answer citing the file it read.

Two `config set` calls fix it, per agent, without weakening anything else. Do this
before the endpoint has more than one caller:
[`../../docs/OPENCLAW-GATEWAY.md#isolation-between-conversations`](../../docs/OPENCLAW-GATEWAY.md#isolation-between-conversations).

---

## Verifying it works

```bash
B=http://127.0.0.1:18090/vireo/message
post() { curl -s -H 'Accept: application/x-ndjson' -d "$1" $B | tail -1; }

post '{"agent":"concierge","session_id":"A1","message":"Remember LANTERN. Reply OK."}'
post '{"agent":"concierge","session_id":"A1","message":"What word?"}'     # → LANTERN
post '{"agent":"concierge","session_id":"A1","message":"Second question"}' # continues
```

The first two lines prove the session mapping: the second turn recalls the first
without the history being resent.

---

## Limits

- **No reply streaming.** One progress line, then the whole answer. The gateway
  streams; forwarding those deltas as `kind: "chunk"` frames is a later change.
- **No tool passthrough.** The gateway's own tools work; a caller's are not
  proxied.
- **No model choice by the caller.** A caller picks a vireo agent; the model is
  gateway configuration, because letting a caller set it is an operator action.
- **No history reconstruction.** If the gateway loses a session, that conversation
  restarts on its side.

---

## When it will not start

Every failure names its own fix.

| Message                                           | Cause                             | Fix                        |
| ------------------------------------------------- | --------------------------------- | -------------------------- |
| `/v1/models is not served`                        | surface disabled                  | the `config set` above     |
| `gateway rejected the token`                      | token is not `gateway.auth.token` | re-export it               |
| `gateway unreachable at …`                        | gateway not running               | `openclaw gateway run`     |
| `config … not found`                              | no YAML                           | it prints the `cp` command |
| `agent "x" has no openclaw agent id`              | `agents.x.id` missing             | add the `id`               |
| `default "x" is not one of the configured agents` | `default` names a missing key     | fix the name               |
| `unknown field`                                   | typo in the YAML                  | the error names the key    |

At runtime: `404` means the surface was disabled again; `401` means the token
rotated; `409` means the gateway session is archived (restore it in the Control UI);
`503` is provider overload; `agent run was cancelled` means vireo timed out — raise
its `call_timeout`.

---

## Tests

```bash
go test ./bridges/openclaw-bridge/
```

The suite pins what must not drift silently: `SessionKey` stability (changing it
orphans every conversation), turn two carrying the same `user` as turn one, no
history resent, a stateless turn sending none, the start message reaching the agent,
ndjson progress, an empty answer becoming an error rather than a blank reply, every
startup failure naming its fix, and — for the no-config mode — that a missing file
is not an error while a missing explicit path is.
