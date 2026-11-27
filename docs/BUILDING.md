# Building and installing vireo

One command. It builds every binary in `cmd/` and `bridges/`, discovering them
rather than listing them, so adding a backend never means editing a build file.

```bash
./scripts/build.sh              # → ./bin
./scripts/build.sh /usr/local/bin
```

```
./bin/vireo
./bin/vireo-chat
./bin/fakeagent
./bin/openclaw-bridge
```

`scripts/dev.sh` calls this for the binaries it needs, and the Dockerfile runs the
same discovery loop, so there is one definition of "what gets built".

## Why there is no list

Two directories are scanned — `cmd/` for tools, `bridges/` for backends — and any
directory with a `main` package becomes a binary named after itself:

```
cmd/vireo-chat              →  bin/vireo-chat
cmd/fakeagent               →  bin/fakeagent
bridges/openclaw-bridge     →  bin/openclaw-bridge
```

The daemon is the root package, the one exception, and it is called `vireo`
because that is what it is.

Adding a backend is therefore: create `bridges/<name>/main.go`, run the build. It
appears. Nothing else needs to know — not the README, not the Dockerfile, not this
document.

## cmd/ versus bridges/

Both produce binaries; the split says what kind of thing it is, which is what
someone browsing the repository needs before opening anything.

| Directory   | What lives there                                | How it is tested           |
| ----------- | ----------------------------------------------- | -------------------------- |
| `bridges/`  | backends — programs that answer vireo's webhook | with whatever they talk to |
| `cmd/`      | tools — clients, stubs, helpers                 | nothing external           |
| `internal/` | libraries the above share                       | unit tests                 |
| `.` (root)  | the daemon                                      | —                          |

A bridge is a unit someone adopts, replaces, or writes themselves, so it gets a
directory of its own with its README, tests and example config together. A tool
like `vireo-chat` is one file and belongs in `cmd/`.

## What each binary is

| Binary            | What it is                             | Needed to run the daemon? |
| ----------------- | -------------------------------------- | ------------------------- |
| `vireo`           | the daemon — the endpoint              | yes                       |
| `vireo-chat`      | terminal client for a daemon           | no, a client              |
| `fakeagent`       | stub backend, canned replies, no model | no, a test backend        |
| `openclaw-bridge` | real backend on an OpenClaw gateway    | no, a backend             |

Only `vireo` is required. The rest are a client and two interchangeable backends —
[what a backend is](BACKENDS.md) explains the difference and how to write one.

## Docker

The image builds the same set and ships `vireo` as its entrypoint:

```bash
docker build -t vireo .
docker run --rm -p 7777:7777/tcp -p 7777:7777/udp \
  -v "$PWD/config:/app/config:ro" \
  -v "$PWD/certs:/app/certs:ro" \
  -v "$PWD/sessions:/app/sessions" \
  vireo --config demo
```

**Every binary is in that one image**, so the others are a flag away:

```bash
docker run --rm --network host \
  -e OPENCLAW_GATEWAY_TOKEN \
  --entrypoint /app/openclaw-bridge \
  vireo -config /app/openclaw-bridge.yaml
```

You do not need a second image per backend. See
[the bridge's Docker notes](../bridges/openclaw-bridge/README.md#docker) for the
network detail that matters: an OpenClaw gateway binds loopback, so a container
needs `--network host` to reach it.

Certificates are not in the image. Mount a pair over `/app/certs` — one from
`scripts/gen-certs.sh` for local use, or your own.
