# openclaw-bridge

A vireo backend that answers through an [OpenClaw](https://openclaw.ai) gateway.

```bash
./bin/openclaw-bridge                 # reads openclaw-bridge.yaml
./bin/openclaw-bridge -config other.yaml -addr :18090
```

```
vireo-chat ──portico/1──▶ vireo ──webhook──▶ openclaw-bridge ──/v1/chat/completions──▶ gateway
```

This is one of two backends in this repository. `fakeagent` returns canned text
so the plumbing can be tested without a model; this one returns real answers. They
are interchangeable as far as vireo is concerned — see
[what a backend is](../../docs/BACKENDS.md).

## Quick start

```bash
# 1. the gateway's OpenAI-compatible surface is OFF by default
openclaw config set gateway.http.endpoints.chatCompletions.enabled true

# 2. the token, which is a secret and does not go in the YAML
export OPENCLAW_GATEWAY_TOKEN="$(jq -r '.gateway.auth.token' ~/.openclaw/openclaw.json)"

# 3. config: copy the shipped example
cp cmd/openclaw-bridge/openclaw-bridge.example.yaml openclaw-bridge.yaml

# 4. run, in this order — the gateway answers the bridge's startup probe
openclaw gateway run &
./bin/openclaw-bridge &
./bin/vireo --config openclaw
```

Or all of it in one command, which is what the Makefile and
[`../dev.sh`](../../scripts/dev.sh) do:

```bash
./scripts/dev.sh --config openclaw
```

## Configuration

`openclaw-bridge.yaml`, taken by `-config` (default: that name in the working
directory). Unknown keys are rejected, so a typo fails at startup instead of being
ignored.

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

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `listen` | string | `:18090` | address to bind |
| `openclaw.base_url` | string | `http://127.0.0.1:18789` | the gateway |
| `openclaw.token` | string | — | optional; prefer `OPENCLAW_GATEWAY_TOKEN` |
| `openclaw.model` | string | `openclaw/default` | default agent target |
| `openclaw.timeout` | duration | `180s` | per-request ceiling toward the gateway |
| `default` | string | — | agent used when the webhook names an unknown one |
| `agents` | map | required | vireo agent name → OpenClaw agent |

Each `agents` entry:

| Key | Type | Meaning |
| --- | --- | --- |
| `id` | string | OpenClaw agent id, **required** |
| `label` | string | human name, logs only |
| `model` | string | target override, e.g. `openclaw/research` |

The map key is the `agent` field of the webhook payload, so it must match a
`name` in the vireo config. One bridge serves several vireo agents; each can point
at a different OpenClaw agent, or at the same one on a different model.

### Why the token is not in this file

It is the gateway's full operator credential — a valid token is equivalent to
owner access to the gateway, and it can drive whatever tools that agent has. A file
meant to be copied and shared should not carry one. `openclaw.token` exists for a
throwaway local setup; `OPENCLAW_GATEWAY_TOKEN` is the intended path, and it wins
over nothing — the file wins if both are set, so an explicit local value is
possible.

### Why the port is 18090

`:8088` is `fakeagent`, `:8089` the ZIWA bridge, `:8090-8094` VS Code's own server
on a developer machine. `:18090` is clear of all of them.

## How it maps sessions

vireo mints a session id; this binary never does. OpenAI's Chat Completions has no
session field, so the id is hashed into the `user` value and OpenClaw derives its
session key from that:

```
vireo session id ──sha256──▶ "vireo-<32 hex>" ──▶ OpenAI `user` ──▶ gateway session
```

- **History is never resent.** The gateway holds it; resending would duplicate
  every earlier turn.
- **Deterministic**, so a bridge restart resumes the same conversations.
- **Lowercase hex**, because the gateway lowercases session keys and two ids
  differing only by case would otherwise collapse into one.
- **No reserved namespace**, since the gateway rejects keys beginning
  `subagent:`, `cron:` or `acp:`.
- **A stateless vireo agent sends no `user`**, so the gateway mints a one-shot
  session and nothing carries over.

Full reasoning, the isolation settings a multi-user deployment needs, and every
failure mode: [`../../docs/OPENCLAW-GATEWAY.md`](../../docs/OPENCLAW-GATEWAY.md).

## What it does not do

- **No reply streaming.** It sends one progress line then the full answer. The
  gateway streams; forwarding those deltas as `kind: "chunk"` frames is a
  later change.
- **No tool passthrough.** The gateway's own tools work; a caller's tools are not
  proxied.
- **No model choice by the caller.** A caller picks a vireo agent; the model is
  gateway configuration, because letting a caller set it is an operator action.
- **No history reconstruction.** If the gateway loses a session, that
  conversation restarts on its side.

## Testing

```bash
go test ./internal/openclaw/
```

The suite pins the parts that must not drift: `SessionKey` stability (changing it
orphans every conversation), turn two carrying the same `user` as turn one, no
history resent, a stateless turn sending none, the start message reaching the agent,
ndjson progress, and every startup failure naming its own fix.
