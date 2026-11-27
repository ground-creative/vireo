# fakeagent

A **stub** backend for vireo. It answers the webhook with canned sentences so the
daemon, the session machinery, the frame format and the client can all be exercised
without a model, an API key, or a network.

```bash
./bin/fakeagent                # :8088
./bin/fakeagent -addr :9090
```

It serves three agents — `orders`, `reservations`, `concierge` — and replies to
each with one of a few fixed lines chosen at random. **It does not read what you
sent.** That is the point: if the answer is wrong, the bug is in vireo or the
client, not in a model.

```
vireo-chat ──portico/1──▶ vireo ──webhook──▶ fakeagent
```

## Endpoints

| Path | Method | What |
| --- | --- | --- |
| `/vireo/message` | POST | the webhook contract; set `Accept: application/x-ndjson` to also get a progress line |
| `/health` | GET | `{"status":"ok"}` — what `scripts/dev.sh` waits on |

## Using it

`config/demo.yaml` already points at it, so the shortest path is:

```bash
./scripts/dev.sh             # starts fakeagent, vireo, and the client
```

By hand:

```bash
./bin/fakeagent &
./bin/vireo --config demo &
./bin/vireo-chat -insecure connect localhost:7777
```

## When to use something else

It proves the pipe, not the product. For a real answer, use a backend that
actually thinks — [`openclaw-bridge`](../../bridges/openclaw-bridge) is one, and
[`../../docs/BACKENDS.md`](../../docs/BACKENDS.md) describes how to write your own.

Note that this backend is **stateless and ignores `session_id`**: it never
remembers a previous turn. A session that works here exercises vireo's envelope,
not any conversation logic.
