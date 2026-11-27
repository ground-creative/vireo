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

**For anything with more than one caller, do the isolation setup first.** A
default personal agent shares conversation content across sessions, so one caller
can recall another's — measured, not assumed. The two commands are in
[Isolation between conversations](#isolation-between-conversations), and they
take a minute.

**Quick local test, single user.** Point the bridge at the existing agent:

```bash
./openclaw-bridge -agent main
```

Fine while you are the only caller. Note that your test conversations land in
`~/.openclaw/workspace/memory/`, which is your personal agent's memory.

**Real use.** Give vireo its own agent, then deny the tools that carry content
between sessions — both steps are in
[Isolation between conversations](#isolation-between-conversations). It appears
as `openclaw/vireo` in `/v1/models`, and the bridge uses it with
`-agent vireo`.

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
unprompted. What does **not** work unprompted is keeping their *content* apart.

This was measured, not assumed. With a default personal install, a value told to
one session was recalled by an unrelated one, and the answer cited the file it
read it from:

```
A: "Remember my vault code QUARTZ-25092"        → OK
B: "What is my vault code?"  [different session]
   → Your vault code is QUARTZ-25092.
     Source: MEMORY.md#L1-L4, memory/2026-11-27.md#L1-L5
```

The cause is not the bridge and not vireo — session keys map correctly. It is
OpenClaw's own context sharing: the `session-memory` hook writes every
conversation into one shared `<workspace>/memory/`, the `memory` source indexes
that directory for all sessions of the agent, and the filesystem tools let the
model read it back.

### The fix: a dedicated agent, configured to be stateless between sessions

Two `config set` calls. Everything below is per-agent, so the personal agent is
not weakened and no gateway-wide default is touched.

**1. Give vireo its own agent and workspace.**

```bash
openclaw config set agents.entries.vireo '{
  "name": "vireo",
  "workspace": "/home/you/.openclaw/agents/vireo/workspace",
  "agentDir": "/home/you/.openclaw/agents/vireo/agent",
  "identity": {"name": "vireo"}
}' --strict-json
```

That alone protects your personal agent — vireo's traffic stops landing in its
workspace — but sessions of the vireo agent still share *its* workspace, so this
is necessary and not yet sufficient.

**2. Deny the tools that carry content across sessions.**

```bash
openclaw config set agents.entries.vireo '{
  "name": "vireo",
  "workspace": "/home/you/.openclaw/agents/vireo/workspace",
  "agentDir": "/home/you/.openclaw/agents/vireo/agent",
  "identity": {"name": "vireo"},
  "memory": {"search": {"rememberAcrossConversations": false, "sources": ["memory"]}},
  "tools": {"deny": [
    "group:sessions", "group:memory",
    "read", "write", "edit", "apply_patch", "exec", "process",
    "browser", "canvas", "memory_search", "memory_get"
  ]}
}' --strict-json
```

Each entry matters:

| Denied | Why |
| --- | --- |
| `group:sessions` | `sessions_search`/`_history` can read **every** session, other users included |
| `group:memory`, `memory_search`, `memory_get` | recall of other conversations' transcripts |
| `read`, `exec`, `process` | the agent shelling out or reading files it should not see |
| `write`, `edit`, `apply_patch` | the agent persisting one session's content where another can find it |
| `browser`, `canvas` | not isolation, but a public endpoint has no use for them |

`rememberAcrossConversations: false` and `sources: ["memory"]` are belt and
braces: with the memory tools denied they cannot be reached anyway, and they stay
correct if the tool policy is later loosened.

Then point the bridge at that agent — this is one flag:

```bash
./openclaw-bridge -agent vireo
```

### Verifying it

Use a value that is unique and not guessable. Ask one session to remember it, and
another to recall it. The second must not know.

```bash
B=http://127.0.0.1:18090/vireo/message
post() { curl -s -H 'Accept: application/x-ndjson' -d "$1" $B | tail -1; }

post '{"agent":"concierge","session_id":"A1","message":"My code is ZEBRA-4417. Reply OK."}'
post '{"agent":"concierge","session_id":"A1","message":"What is my code?"}'        # → ZEBRA-4417
post '{"agent":"concierge","session_id":"B1","message":"What is my code? Say UNKNOWN if you do not know."}'
```

The third must say `UNKNOWN`. If it does not, a tool from the deny list is still
available to that agent.

### What this costs

The agent can no longer use files, shell, or memory search. For a conversational
endpoint that is the right trade, and it is the only configuration that was
verified to hold: denying `group:sessions` and `group:memory` alone left the
leak, because the agent simply read the shared file with `exec`/`read` instead.

Test values still land in the vireo agent's own `memory/` directory, harmless to
other callers but worth clearing if you reuse names.

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
