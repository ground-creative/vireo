# Backends

vireo is the endpoint; a **backend** is whatever answers the messages it forwards.
The daemon does not know what is behind a `transport` URL, and does not care.

```
client ──portico/1──▶ vireo ──HTTP POST──▶ backend
                              /vireo/message
```

## The contract

One endpoint, one request, one response.

```http
POST /vireo/message
Content-Type: application/json
Accept: application/x-ndjson, application/json
X-Portico-Agent: <agent name>
X-Portico-Session: <vireo's session id>       (absent for a stateless agent)
X-Portico-Protocol: portico/1

{"agent":"concierge","session_id":"…","message":"…","initial_assistant_message":"…"}
```

```json
{"reply":"…","session":{"summary":"one line for the conversation list"}}
```

`initial_assistant_message` is set **only on the first turn** — it is the greeting
the client already showed, so the backend knows what was said.

The body may also be newline-delimited JSON: `{"partial":"…"}` lines while it
works, then the final `{"reply":…}`. `vireo` always sends
`Accept: application/x-ndjson`, so a slow backend should use it — otherwise a long
turn looks like a hang. The client shows each `partial` immediately.

Full field-level rules are in [`PROTOCOL.md`](PROTOCOL.md); the normative text is
in the [specification](https://github.com/ground-creative/agent-front-door-spec).

## In this repository

Two, and they are deliberately different in kind.

| Backend | Answers with | Use it for |
| --- | --- | --- |
| [`fakeagent`](../cmd/fakeagent) | canned text, no model, no key | testing the daemon, the session machinery, the wire |
| [`openclaw-bridge`](../cmd/openclaw-bridge) | a real agent through an OpenClaw gateway | the smallest complete deployment |

Both are `cmd/<name>` directories. Adding a third is a new directory and nothing
else — [`scripts/build.sh`](../scripts/build.sh) discovers it, so no build file,
README or Dockerfile lists binaries.

## Writing one

Any HTTP server that speaks the contract above will do. The two here are Go only
because the repository is Go; the protocol is JSON over HTTP.

Three things are worth getting right, and they are the three `fakeagent` gets
wrong on purpose:

1. **Sessions are the backend's problem, not vireo's.** vireo mints an id and
   passes it on every turn; keeping the conversation is up to you. `openclaw-bridge`
   maps the id onto the gateway's own session key and lets the gateway hold the
   history. A backend with no state can ignore the field entirely.
2. **Say "hold on" when a turn is slow.** Emit a `partial` line before the work
   starts. A client cannot tell a slow answer from a dead connection.
3. **Report a summary.** `session.summary` is what a caller sees in their
   conversation list — one line, in the answer's own terms. Without it the list
   shows "(no summary yet)", which is a worse experience than a rough summary.

Reference implementation, about 400 lines including comments:
[`internal/openclaw`](../internal/openclaw). The startup probe it does — check the
surface is reachable and say which fix is needed — is worth copying; a backend that
fails per-request with a 404 is much harder to diagnose than one that refuses to
start.

## Configuring vireo for one

```yaml
transport: "http://127.0.0.1:8088/vireo/message"   # default, inherited

agents:
  - name: "concierge"
    description: "Ask anything"
    sessions: true                                 # keeps a conversation
```

An agent may override `transport` when it lives elsewhere. An agent with
`sessions: false` is stateless: vireo mints nothing, no `X-Portico-Session` is
sent, and the backend must not keep history.

`name` is what a caller sees and what arrives as `X-Portico-Agent`, so the backend
must recognise it. See [`CONFIGURATION.md`](CONFIGURATION.md) for the rest.
