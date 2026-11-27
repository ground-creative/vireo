# Building and installing vireo

One command. It builds every binary in `cmd/`, discovers them rather than listing
them, so adding a backend never means editing a build file.

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

`scripts/dev.sh` calls this for the binaries it needs, and `scripts/release.sh`
calls it for the archives, so there is one definition of "what gets built".

## Why there is no list

`cmd/` is the definition. Anything with a `main` package there is a binary, and it
is named after its directory. That is the whole rule:

```
cmd/vireo-chat         →  bin/vireo-chat
cmd/fakeagent          →  bin/fakeagent
cmd/openclaw-bridge    →  bin/openclaw-bridge
```

The daemon itself is the root package, which is the one exception, and it is
called `vireo` because that is what it is.

Adding a backend is therefore: create `cmd/<name>/main.go`, run the build. It
appears. Nothing else in the repository needs to know about it — not the README,
not the Dockerfile, not this document.

## What each binary is

| Binary | What it is | Needed to run the daemon? |
| --- | --- | --- |
| `vireo` | the daemon — the endpoint | yes |
| `vireo-chat` | terminal client for a daemon | no, a client |
| `fakeagent` | stub backend, canned replies, no model | no, a test backend |
| `openclaw-bridge` | real backend on an OpenClaw gateway | no, a backend |

Only `vireo` is required. The rest are a client and two interchangeable backends —
[what a backend is](../docs/BACKENDS.md) explains the difference and how to write
one.

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

Certificates are not in the image. Mount a pair over `/app/certs` — one from
`scripts/gen-certs.sh` for local use, or your own.
