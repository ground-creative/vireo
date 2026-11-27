# Local testing

One command runs a whole stack locally:

```bash
./scripts/dev.sh                     # daemon + client, with the stub backend
./scripts/dev.sh --backend openclaw  # the same, plus the gateway and bridge
```

That is the whole procedure. It generates a certificate if there is none, builds
the binaries, starts what the backend needs, connects `vireo-chat`, and stops
everything when the client exits.

There is one committed config, `config/demo.yaml`. `--backend openclaw` writes a
matching vireo config to `config/dev-openclaw.yaml` for the run and deletes it on
exit, so the repository does not carry a second YAML that differs only in a port
and a transport.

```
▸ starting the OpenClaw bridge (:18090)
[bridge] openclaw bridge listening on :18090 ...
▸ starting the daemon (vireo --config dev-openclaw, port 7779)
[vireo] listening on [::]:7779 (tcp/tls, ...)
▸ connecting vireo-chat — Ctrl-C stops every process above
```

Every process's output is prefixed with its name, so a failure is attributable at
a glance instead of being four interleaved streams.

## Why the certificate exists

The daemon is **TLS-only** — there is no plaintext mode and no way to start it
without `cert_file` and `key_file`. So a certificate is a structural requirement of
running the process, not a security measure you opted into for a test.

`scripts/dev.sh` generates one with `scripts/gen-certs.sh` if `certs/` is empty.
It is self-signed, valid for `localhost`, `127.0.0.1` and `::1` for ten years.

## Why `-insecure` is fine here

The script connects with `-insecure`, and that is the correct choice for a local
run. What the flag turns off is the check on *who* answered. On a loopback socket
to a daemon started a second ago by the same user, there is no one else to be.

Putting that plainly, because the alternative is worse advice:

| Situation | Right tool | Why |
| --- | --- | --- |
| Loopback, your daemon, your machine | `-insecure` | Nothing to authenticate against |
| Local, but the same config may later point at a real deployment | `-ca` or a public cert | The config outlives the test |
| Anything shared, networked, or multi-user | `-ca` or a public cert | The peer's identity matters |

The risk with `-insecure` is **not** the local test. It is that the flag gets
copied into a config that is later pointed somewhere that matters, and nobody
reads it again. Use it where it is disposable.

## If you want verification locally

`-ca` trusts one specific certificate, with verification left on:

```bash
./vireo-chat -ca certs/cert.pem connect localhost:7777
```

The client still checks that the peer presents a certificate signed by that file
**and** covering the name you asked for. A different certificate is rejected, and
so is a trusted certificate for the wrong hostname — both are covered by tests in
`internal/client/cafile_test.go`.

For an MCP host, the same thing as an argument:

```json
{
  "mcpServers": {
    "vireo": { "command": "vireo-mcp", "args": ["-ca", "/abs/path/to/certs/cert.pem"] }
  }
}
```

One caveat: `-ca` points at a file in the repo. Regenerating certificates
replaces it, and a long-lived client (an MCP server, say) keeps the roots it
loaded at startup — so it must be restarted after `gen-certs.sh` runs. That is
the honest cost of verification, and the reason `-insecure` is the smoother
choice when the only goal is to see the thing work.

## What verification does and does not buy

Worth being precise, because a "digest verified" line in the manifest output is
easy to misread.

- **The digest is not authentication.** `sha256` over the manifest proves the
  document arrived intact and that publisher and reader hashed the same bytes
  (MAN-40). Under `-insecure`, an impostor would serve its own manifest and that
  line would still print "digest verified".
- **Certificate verification is authentication.** It is what binds the connection
  to the endpoint you meant to reach. With it off, the traffic is still encrypted,
  but only against *whoever answered*.

So: locally, `-insecure` costs you nothing real. It just means the "digest
verified" line no longer implies the peer is the daemon you think it is.

## Without the script

The script is a convenience, not a requirement. The same three processes by hand:

```bash
./scripts/gen-certs.sh
./scripts/build.sh
./bin/fakeagent                # stub backend, :8088
./bin/vireo --config demo      # daemon, :7777
./bin/vireo-chat -insecure connect localhost:7777
```

For the OpenClaw path there are two more, and their order matters — the gateway
answers the bridge's startup probe, so it must be listening first:

```bash
export PATH="$HOME/.nvm/versions/node/v24.22.3/bin:$PATH"
export PATH="$HOME/.npm/_npx/87115a8ab6c363bd/node_modules/.bin:$PATH"
openclaw gateway run                                 # :18789
export OPENCLAW_GATEWAY_TOKEN="$(jq -r '.gateway.auth.token' ~/.openclaw/openclaw.json)"
./bin/openclaw-bridge                                # :18090 (example config)
./scripts/dev.sh --backend openclaw                  # or let the script do all of it
```

See [`bridges/openclaw-bridge/README.md`](../bridges/openclaw-bridge/README.md) for the
bridge — install, run, every config key — and
[`OPENCLAW-GATEWAY.md`](OPENCLAW-GATEWAY.md) for the gateway side and the
isolation settings a multi-user deployment needs.
