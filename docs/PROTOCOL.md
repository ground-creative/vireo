# vireo wire protocol

> **Superseded.** The normative protocol specification now lives in the
> Agent Front Door specification repository:
> [`agent-front-door-spec/normative/PROTOCOL.md`](https://github.com/ground-creative/agent-front-door-spec/blob/main/normative/PROTOCOL.md).
> That document assigns a requirement ID (`PROTO-n`, `WH-n`) to every rule and is
> written so an implementation in any language can follow it without reading this
> codebase. This copy is kept as the implementation's own notes; when the two
> disagree, the specification wins.

One port serves both transports:

- **TCP + TLS 1.3** and **UDP + QUIC**, same port (default `7777`)
- ALPN `frontdoor/1` selects the protocol
- Every message is a frame: 9-byte header, then a JSON payload

## Frames

| Byte | Field                | Meaning                            |
| ---- | -------------------- | ---------------------------------- |
| 0–3  | `stream_id` (uint32) | correlates request/response stream |
| 4    | `type` (uint8)       | frame type                         |
| 5–8  | `length` (uint32)    | payload length, max 8 MiB          |
| 9–   | payload              | UTF-8 JSON                         |

All integers are big-endian. A reader consumes exactly one frame per call
regardless of how the transport splits bytes; a payload cut short mid-frame is an
error, not EOF.

| Type        | Code   | Payload                    |
| ----------- | ------ | -------------------------- |
| `message`   | `0x01` | `MessageRequest`           |
| `data`      | `0x02` | `MessageResponse`          |
| `end`       | `0x03` | `{ok, cancelled, session}` |
| `partial`   | `0x04` | `{kind, seq, text}`        |
| `error`     | `0x05` | `{code, message}`          |
| `negotiate` | `0x10` | `{protocol, client}`       |
| `manifest`  | `0x11` | manifest                   |
| `session`   | `0x12` | `SessionRequest`           |
| `cancel`    | `0x13` | empty                      |

A `partial` frame is progress inside a turn: text the agent wants shown while it
is still working. It never ends the turn and never counts as one — `end` still
follows, and only that increments the turn count. Old clients ignore the type,
so it is additive. `kind` is `status` for an ephemeral line ("fetching the
site…"); `seq` orders the frames within one turn.

A `cancel` frame abandons the turn on the stream it names. It is additive in the
same way: a daemon that does not know the type rejects it as `invalid_frame`, and
a caller that never sends one is unaffected. See
[Cancelling a turn](#cancelling-a-turn).

## Handshake

The first frame on a connection must be `negotiate`:

```json
{
  "protocol": "frontdoor/1",
  "client": {
    "id": "vireo_4f1c8a2e9b7d4c0fa1e2d3b4c5a6f7e8",
    "name": "vireo-chat",
    "version": "0.4"
  }
}
```

`client.id` is the identity sessions are scoped by, so its shape is fixed:

```
^vireo_[0-9a-f]{32}$
```

A `vireo_` prefix, then 32 lowercase hex characters — 128 random bits. Anything
else is refused with `bad_client_id`: too short, wrong case, a UUID, an email
address, `me`. Peers generate one once and keep it; only `vireo-mcp` and
`vireo-chat` mint ids in practice.

A protocol other than `frontdoor/1` is refused with `bad_protocol`. Any first frame
of another type is `invalid_frame`.

The daemon answers on the same stream with a `manifest`:

```json
{
  "protocol": "frontdoor/1",
  "endpoint": {
    "id": "restaurant-dev",
    "name": "restaurant",
    "description": "Fresh pizza and table bookings"
  },
  "commands": [
    "session.close",
    "session.get",
    "session.list",
    "session.new",
    "session.prune"
  ],
  "agents": [
    {
      "name": "orders",
      "description": "Take food orders: pizzas, sides, drinks, pickup or delivery",
      "sessions": true,
      "refs": { "menu": "http://127.0.0.1:8088/menu" }
    },
    {
      "name": "concierge",
      "description": "Answer anything else: hours, directions, general questions",
      "sessions": true
    }
  ],
  "refs": {
    "menu": "http://127.0.0.1:8088/menu",
    "docs": "https://github.com/your-org/your-service"
  },
  "session": { "ttl_seconds": 3600, "list_enabled": true }
}
```

`agents[].sessions` says whether the agent holds multi-turn conversations.
`refs` are static URLs a peer can follow; they appear both on the endpoint as a
whole and per agent.

A `negotiate` frame arriving later is answered with the manifest again (no
rejection); any other unexpected frame type gets `invalid_frame`.

## Messages

A `message` frame carries one turn of conversation:

```json
{
  "agent": "orders",
  "session_id": "y113YjxvfPQzaE6ZWhmabl-wnxthBAFJ",
  "message": "I'd like 2 margherita pizzas for pickup"
}
```

- `agent` is required and must name an agent from the manifest.
- `message` is required and is the text.
- `session_id` continues an existing conversation. A conversational agent
  (`sessions: true`) requires one; a stateless agent must not have one.

The daemon answers with a `data` frame carrying the reply and an `end` frame
carrying the updated envelope:

```json
{
  "reply": "Sure — margherita, marinara and tiramisu are on the menu today.",
  "session": { "summary": "took an order request" }
}
```

```json
{
  "ok": true,
  "session": {
    "id": "y113Y...",
    "turns": 1,
    "summary": "took an order request",
    "status": "open"
  }
}
```

The agent's `session.summary` replaces the envelope's summary, and
`session.status: "closed"` closes the conversation after the turn.

## Sessions

A session is an **envelope**, never conversation content:

```json
{
  "id": "y113YjxvfPQzaE6ZWhmabl-wnxthBAFJ",
  "owner": "me",
  "agent": "orders",
  "created_at": "2026-11-13T16:04:31.217549762+07:00",
  "updated_at": "2026-11-13T16:04:31.218765345+07:00",
  "expires_at": "2026-11-13T17:04:31.218765345+07:00",
  "summary": "took an order request",
  "turns": 1,
  "status": "open"
}
```

`status` is `open`, `closed`, or `expired`. Envelopes are scoped by
`(client id, agent, session id)`: an id from another client behaves as unknown.
Only `open` sessions can be resumed. The daemon mints ids; peers echo them back.

Session management rides on its own frame so the message channel stays free of
anything but conversation. The payload is `SessionRequest`:

| Op              | Fields                       | Answer                                |
| --------------- | ---------------------------- | ------------------------------------- |
| `session.new`   | `agent`, `summary?`          | `{session, start_message?}`           |
| `session.list`  | `agent`, `limit?`, `cursor?` | `{sessions, next_cursor}`             |
| `session.get`   | `agent`, `id`                | the envelope                          |
| `session.close` | `agent`, `id`, `reason?`     | the closed envelope                   |
| `session.prune` | `retention?`                 | `{retention, removed, before, after}` |

The ops are advertised in `manifest.commands` when `session.list` is enabled.

## Webhook

The daemon POSTs every message to the agent's `transport`:

```
POST /vireo/message
Content-Type: application/json
X-Frontdoor-Protocol: frontdoor/1
X-Vireo-Agent: orders
X-Vireo-Session: y113YjxvfPQzaE6ZWhmabl-wnxthBAFJ

{
  "agent": "orders",
  "session_id": "y113YjxvfPQzaE6ZWhmabl-wnxthBAFJ",
  "message": "I'd like 2 margherita pizzas for pickup"
}
```

When the agent declares a `start_message`, the daemon returns it to the client
at session creation — no backend call is made for it — and sends it to the
backend on the first turn as `initial_assistant_message`:

```json
{
  "agent": "orders",
  "session_id": "y113YjxvfPQzaE6ZWhmabl-wnxthBAFJ",
  "message": "I'd like 2 margherita pizzas for pickup",
  "initial_assistant_message": "Welcome to the orders desk."
}
```

A model-backed backend uses it as the assistant turn the client already saw,
so the greeting is not repeated. Later turns omit the field.

The backend answers with any 2xx and JSON:

```json
{
  "reply": "Sure — margherita, marinara and tiramisu are on the menu today.",
  "session": { "summary": "took an order request" }
}
```

A bare JSON string is accepted as the reply. A non-2xx status fails the message
with `upstream_error`, and response bodies are capped at 8 MiB.

If the caller cancels the turn, the daemon cancels the request context. The
backend observes it as `r.Context().Done()` — on the standard library's
`*http.Request`, that is the signal. A backend that wants to act on it (abandon a
model call, stop a build) does so there; one that ignores it simply finishes and
its answer is discarded.

### Progress: saying "hold on"

A backend that knows a turn will be slow can stream the answer instead of
waiting to produce it. If the request carries

```
Accept: application/x-ndjson
```

answer with `Content-Type: application/x-ndjson` and newline-delimited JSON:

```
{"partial": "fetching your site…"}
{"partial": "building the assistant…"}
{"reply": "Done — your assistant is live.", "session": {"summary": "created an agent"}}
```

Every `partial` line is forwarded to the caller immediately as a `partial` frame;
the last line is the usual `{reply, session}`. The turn still ends with `end`, so
progress never counts as a turn and never changes the envelope. Anything that
does not answer with that content type keeps the whole-body behaviour above, and
a backend may ignore the header entirely — the daemon then simply waits.

## Cancelling a turn

A caller that no longer wants an answer sends a `cancel` frame naming the turn
to abandon:

```
stream_id = 3   type = 0x13   payload = (empty)
```

`stream_id` addresses the turn: it is the stream the `message` frame was sent
on. No other field is needed, so a cancel cannot be aimed at the wrong
conversation.

What the daemon does:

1. cancels the context of the upstream webhook call for that stream, which
   closes the backend request;
2. stops waiting for the answer;
3. answers that stream with an `end` frame and **no** `data` frame.

```json
{ "ok": false, "cancelled": true }
```

A cancelled turn is not a turn: the session envelope is not touched, so `turns`
and the summary stay exactly as they were. Cancelling a stream that is not in
flight is harmless — a late cancel is ignored rather than reported as an error.

Cancellation is **cooperative and best effort, never a rollback**. The daemon
can stop waiting and can abort the backend request, but it cannot undo work the
backend already committed: an assistant that was already provisioned stays
provisioned. A backend sees the cancellation as its HTTP request context being
done — see [Webhook](#webhook). What it does with that is its own decision.

Because turns run concurrently, the read loop stays free while a call is in
flight. That is what makes a cancel frame readable at all; a caller sending one
on a slow turn is not waiting for the turn to finish first.

## Discovery

Clients resolve `_frontdoor._tcp.<domain>`:

```
_frontdoor._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

There is no default port and no fallback. A hostname with no SRV record is
refused; address the daemon as `host:port` instead. Guessing a port would make
a typo in a hostname look like a daemon that is down.

## Error codes

| Code               | When                                                   |
| ------------------ | ------------------------------------------------------ |
| `bad_client_id`    | the client id does not match `^vireo_[0-9a-f]{32}$`    |
| `bad_protocol`     | negotiated protocol is not `frontdoor/1`                   |
| `agent_not_found`  | no agent with that name                                |
| `session_required` | a conversational agent was addressed without a session |
| `unknown_session`  | the session id is unknown, closed, or another client's |
| `upstream_error`   | the webhook failed, timed out, or returned non-2xx     |
| `invalid_frame`    | malformed frame or payload                             |
| `internal`         | anything unexpected                                    |

Errors arrive as an `error` frame on the caller's stream:

```json
{
  "code": "session_required",
  "message": "agent \"orders\" holds conversations: start one first (session.new)"
}
```
