# Configuration

Everything the daemon reads lives in `config/`. One file per endpoint, selected
by name:

```bash
./vireo --config demo      # reads config/demo.yaml
```

`--config` is required. `demo`, `demo.yaml` and `config/demo.yaml` are the same
file; `.yaml` is implied. A path outside `config/` is refused:

```
config: "../elsewhere.yaml" is outside config/ — configs live in config/ only
```

Every file in `config/` except `demo.yaml` is gitignored, so personal and
production configs never get committed.

## Full example

```yaml
server:
  port: 7777
  cert_file: "certs/cert.pem"
  key_file: "certs/key.pem"
  alpn:
    - "vireo/1"
  endpoint:
    id: "restaurant-prod"
    name: "restaurant"
    description: "Fresh pizza and table bookings"

transport: "https://your-host.example/vireo/message"

refs:
  menu: "https://your-host.example/menu"
  docs: "https://your-host.example/docs"

session:
  list: true
  ttl_default: 3600
  store: "file"
  file: "vireo-sessions.json"
  retention: "720h"

agents:
  - name: "orders"
    description: "Take food orders: pizzas, sides, drinks, pickup or delivery"
    sessions: true
    refs:
      menu: "https://your-host.example/menu"

  - name: "reservations"
    description: "Check availability, book a table and cancel a booking"
    sessions: true
    transport: "https://another-host.example/vireo/message"
    timeout: "20s"

  - name: "status"
    description: "Opening hours and current waiting time"
    sessions: false
```

## `server`

| Key                    | Type     | Default          | Meaning                                                               |
| ---------------------- | -------- | ---------------- | --------------------------------------------------------------------- |
| `port`                 | int      | —                | TCP and UDP port; unset means the OS assigns one                      |
| `cert_file`            | string   | —                | PEM certificate chain                                                 |
| `key_file`             | string   | —                | PEM private key                                                       |
| `alpn`                 | list     | `["vireo/1"]`    | negotiated protocols; `h3` is dropped with a warning                  |
| `call_timeout`         | duration | derived          | global ceiling for agent calls; default = longest agent timeout + 15s |
| `endpoint.id`          | string   | —                | stable id peers see in the manifest                                   |
| `endpoint.name`        | string   | first agent name | display name                                                          |
| `endpoint.description` | string   | —                | one-line description                                                  |

TLS 1.3 is required. There is no plaintext mode.

## `rate_limit`

Optional per-client rate limiting (token bucket). The block decides everything:

| Config                          | Effect                                   |
| ------------------------------- | ---------------------------------------- |
| no `rate_limit` block           | limiting off                             |
| `rate_limit:` block             | limiting on, with the defaults below     |
| `rate_limit: {enabled: false}`  | limiting off, explicitly                 |

| Key                   | Type | Default | Meaning                                    |
| --------------------- | ---- | ------- | ------------------------------------------ |
| `enabled`             | bool | `true`  | master switch (only written to turn it off) |
| `requests_per_minute` | int  | `60`    | sustained rate per client per agent        |
| `burst`               | int  | `10`    | token bucket depth (allows short bursts)   |
| `agents`              | map  | `{}`    | per-agent overrides (keys are agent names) |

Per-agent overrides inherit missing fields from the global values; a field set
to `0` is treated as missing, so there is no way to express "zero requests" for
one agent — disable limiting globally or pick a low rate instead.

```yaml
server:
  rate_limit:
    enabled: true
    requests_per_minute: 60
    burst: 10
    agents:
      agent-creator:
        requests_per_minute: 10
        burst: 2
      sales-representative:
        requests_per_minute: 30
        burst: 5
```

When a client exceeds the limit, the daemon returns error code `rate_limited`
with a message indicating the retry delay.

## `transport`

Default webhook for every agent: `https://host/path`. An agent may override it
with its own `transport`. An agent with no transport anywhere is skipped at
startup with a warning.

## `refs`

A map of static URLs published in the manifest, at the endpoint level and per
agent. Any keys you like — a menu, docs, a status page. A peer receives them in
the manifest and can follow them without asking you.

## `session`

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

`file` survives restarts (the map is rewritten atomically on every change);
`memory` does not. Session state is bound to `sessions/`: `file` must be a bare
name, and the directory and file are created on first write.

`retention` prunes only finished records — closed past `updated_at`, expired
past `expires_at`. Open sessions are never removed. With no retention set, a
sweeper never runs.

Mint rules are validated at startup, so a contradictory `prefix` / `charset` /
`length` / `regex` fails before the daemon listens.

## `agents`

| Key             | Type     | Default         | Meaning                                                         |
| --------------- | -------- | --------------- | --------------------------------------------------------------- |
| `name`          | string   | —               | unique agent name; required                                     |
| `description`   | string   | —               | what the agent handles — this is what an AI reads when choosing |
| `sessions`      | bool     | `true`          | whether the agent holds multi-turn conversations                |
| `transport`     | string   | top-level       | this agent's webhook                                            |
| `timeout`       | duration | `30s`           | per-message backend timeout                                     |
| `start_message` | string   | —               | greeting returned at session creation                           |
| `refs`          | map      | —               | static URLs for this agent                                      |
| `session`       | block    | inherits global | per-agent `ttl` and mint overrides                              |

A conversational agent (`sessions: true`) requires a session id on every
message; a stateless agent (`sessions: false`) answers single messages and
records nothing.

### `start_message`

A greeting the agent says when a conversation starts. It is part of the
configuration, so it is delivered to the client the moment the conversation is
created — no backend call is made for it:

```yaml
agents:
  - name: "concierge"
    description: "Ask the restaurant's AI anything"
    start_message: "Hello! How can I help you with your reservation?"
    sessions: true
```

The client shows it right away, so a conversation starts with the agent's
words rather than with silence. On the first turn the daemon also tells the
backend what was said, in the `initial_assistant_message` field of the webhook
payload — so a model-backed agent knows the greeting is already in the history
and does not repeat it. Later turns do not carry the field.

Without a `start_message` nothing changes: the conversation starts empty and
the backend answers the first message as usual.

### Per-agent session overrides

```yaml
agents:
  - name: "reservations"
    session:
      ttl: 86400
      prefix: "bk-"
      length: 6
      charset: "0123456789"
      regex: "^bk-[0-9]{6}$"
```

Properties inherit independently from the global `session` block. Changing
`length`, `prefix` or `charset` without a `regex` re-derives the regex instead of
inheriting one that would contradict them.

## Startup validation

A broken agent does not abort startup — it is skipped and the rest keep serving,
each failure reported with the entry's position and name:

```
warning: agents[1]: name is required
warning: agents[2] "reservations": no transport (agent-level or top-level)
warning: agents[3] "orders": invalid timeout "soon": time: invalid duration "soon"
warning: agents[4] "orders": agent "orders" is configured twice
```

Fatal at startup: an unreadable config, a bad certificate, an invalid
`session.retention`, an invalid `session.file`, a missing `--config`.

## File layout

```
config/           the only directory read; demo.yaml is the committed example
certs/            certificate and key
sessions/         runtime envelopes (gitignored, .gitkeep only in git)
```
