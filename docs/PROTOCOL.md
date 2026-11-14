# vireo wire protocol

One port serves both transports:

- **TCP + TLS 1.3** and **UDP + QUIC**, same port (default `7777`)
- ALPN `vireo/1` selects the protocol
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

| Type        | Code   | Payload              |
| ----------- | ------ | -------------------- |
| `message`   | `0x01` | `MessageRequest`     |
| `data`      | `0x02` | `MessageResponse`    |
| `end`       | `0x03` | `{ok, session}`      |
| `error`     | `0x05` | `{code, message}`    |
| `negotiate` | `0x10` | `{protocol, client}` |
| `manifest`  | `0x11` | manifest             |
| `session`   | `0x12` | `SessionRequest`     |

## Handshake

The first frame on a connection must be `negotiate`:

```json
{
  "protocol": "vireo/1",
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

A protocol other than `vireo/1` is refused with `bad_protocol`. Any first frame
of another type is `invalid_frame`.

The daemon answers on the same stream with a `manifest`:

```json
{
  "protocol": "vireo/1",
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
X-Vireo-Protocol: vireo/1
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

## Discovery

Clients resolve `_vireo._tcp.<domain>`:

```
_vireo._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

There is no default port and no fallback. A hostname with no SRV record is
refused; address the daemon as `host:port` instead. Guessing a port would make
a typo in a hostname look like a daemon that is down.

## Error codes

| Code               | When                                                   |
| ------------------ | ------------------------------------------------------ |
| `bad_client_id`    | the client id does not match `^vireo_[0-9a-f]{32}$`    |
| `bad_protocol`     | negotiated protocol is not `vireo/1`                   |
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
