# OpenClaw gateway setup

What has to be true on the **gateway** side for
[`openclaw-bridge`](../bridges/openclaw-bridge) to work, and what it must be told to
keep conversations apart.

The bridge itself — its config, its session mapping, what it does not do — is
documented next to the code: [`bridges/openclaw-bridge/README.md`](../bridges/openclaw-bridge/README.md).

## 1. Enable the OpenAI-compatible surface

It is **disabled by default**, which is the first thing that goes wrong:

```bash
openclaw config set gateway.http.endpoints.chatCompletions.enabled true
```

Applies without a restart. Verify:

```bash
curl -s http://127.0.0.1:18789/v1/models \
  -H "Authorization: Bearer $OPENCLAW_GATEWAY_TOKEN"
```

`/v1/models` returns the agent targets the gateway serves (`openclaw`,
`openclaw/default`, `openclaw/<agentId>`). A `404` here means the surface is still
off; the bridge refuses to start and says so rather than failing per message.

## 2. Decide which agent vireo talks to

By default the bridge targets `openclaw/<id>` from its own config. Two situations:

**Personal gateway, quick test.** Point it at the existing agent (`id: "main"`).
Fast, and fine if you are the only caller — but read the isolation section below
first, because on a personal install that agent's memory spans sessions.

**Anything with more than one caller.** Give vireo its own agent, so its traffic
never enters your personal agent's workspace or memory:

```bash
openclaw config set agents.entries.vireo '{
  "name": "vireo",
  "workspace": "/home/you/.openclaw/agents/vireo/workspace",
  "agentDir": "/home/you/.openclaw/agents/vireo/agent",
  "identity": {"name": "vireo"}
}' --strict-json
```

It then appears as `openclaw/vireo` in `/v1/models`, and the bridge config uses
`id: "vireo"`.

## 3. The token

The bridge authenticates with `gateway.auth.token` — the **operator** credential,
not a provider API key and not a scoped one. A valid token is equivalent to owner
access: it can drive whatever tools that agent has.

```bash
export OPENCLAW_GATEWAY_TOKEN="$(jq -r '.gateway.auth.token' ~/.openclaw/openclaw.json)"
```

Keep it out of YAML that might be committed. The bridge's own config documents the
`openclaw.token` field that exists for a throwaway local setup.

## Isolation between conversations

Separate vireo sessions become separate gateway sessions — that part works
unprompted. But OpenClaw has three features that deliberately share context
**across** sessions, and on a personal install each one leaks one conversation into
another.

This is not theoretical. With all three at their defaults, a value told to one
session was recalled by an unrelated one, and the answer cited the other session's
transcript as its source.

| Feature                                         | Default       | What it does                                                                           | To isolate |
| ----------------------------------------------- | ------------- | -------------------------------------------------------------------------------------- | ---------- |
| `memory.search.rememberAcrossConversations`     | on (personal) | `memory_search` recalls other conversations' transcripts                               | `false`    |
| `tools.sessions.visibility`                     | `all`         | `sessions_search`/`_history` reach **every** session, other users included             | `"self"`   |
| `hooks.internal.entries.session-memory.enabled` | on            | writes excerpts to a **shared** workspace `MEMORY.md`, which the memory source indexes | `false`    |

```bash
openclaw config set memory.search.rememberAcrossConversations false --strict-json
openclaw config set tools.sessions.visibility '"self"' --strict-json
openclaw config set hooks.internal.entries.session-memory.enabled false --strict-json
```

The first two apply without a restart. These are gateway-wide, so they also weaken
your personal agent — which is why the dedicated agent above is the better answer
for anything beyond a local test.

### Verifying isolation

Use a value that is unique and not guessable, ask one conversation to remember it,
then ask another. The second must say it does not know.

```bash
B=http://127.0.0.1:18090/vireo/message
post() { curl -s -H 'Accept: application/x-ndjson' -d "$1" $B | tail -1; }

post '{"agent":"concierge","session_id":"A1","message":"My code is ZEBRA-4417. Reply OK."}'
post '{"agent":"concierge","session_id":"A1","message":"What is my code?"}'
post '{"agent":"concierge","session_id":"B1","message":"What is my code? Say UNKNOWN if you do not know."}'
```

The third must not contain `ZEBRA-4417`. If it does, one of the three settings above
is still at its default.

The `session-memory` hook writes to `<workspace>/memory/`, and the workspace is
shared by every session of that agent. Test values land in that file — remove them
afterwards, or use the dedicated agent.

## Failure modes

At bridge startup:

| Message                                           | Cause                             | Fix                        |
| ------------------------------------------------- | --------------------------------- | -------------------------- |
| `/v1/models is not served`                        | surface disabled                  | the `config set` in step 1 |
| `gateway rejected the token`                      | token is not `gateway.auth.token` | re-export it               |
| `gateway unreachable at …`                        | gateway not running               | `openclaw gateway run`     |
| `agent "x" has no openclaw agent id`              | `agents.x.id` missing             | add the `id`               |
| `default "x" is not one of the configured agents` | `default` names a missing key     | fix the name               |

At runtime:

| Message                                 | Meaning                                                             |
| --------------------------------------- | ------------------------------------------------------------------- |
| `gateway 404`                           | the surface was disabled again                                      |
| `gateway 401`                           | token rotated; restart the bridge                                   |
| `gateway 409 [… invalid_request_error]` | the gateway session is archived; restore it in the Control UI       |
| `gateway 503 api_error`                 | provider overload                                                   |
| `gateway returned an empty answer`      | the agent produced no text; surfaced as an error, not a blank reply |
| `agent run was cancelled`               | vireo timed out and cancelled; raise `call_timeout`                 |

## Operating notes

- **`openclaw gateway run` is foreground.** It holds the terminal; closing that
  terminal stops the gateway. `openclaw gateway restart` can leave it down when no
  systemd unit is installed — prefer stopping and running again.
- **The `openclaw` CLI may not be on `PATH`** when installed through `npx`:

  ```bash
  export PATH="$HOME/.nvm/versions/node/v24.22.3/bin:$PATH"
  export PATH="$HOME/.npm/_npx/87115a8ab6c363bd/node_modules/.bin:$PATH"
  ```

  Node 24 avoids a `node:sqlite` truncation warning that Node 22 prints.

- **The gateway's docs ship locally**, under
  `<npx-dir>/node_modules/openclaw/docs/`:
  `gateway/openai-http-api.md`, `reference/memory-config.md`,
  `gateway/config-tools/sessions-and-subagents.md`.
