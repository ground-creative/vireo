# Deployment

The deployment is a static binary plus three directories: `config/`, `certs/`
and a writable `sessions/`.

## Build

Native:

```bash
go build -trimpath -ldflags="-s -w" -o vireo .
```

Cross-compile for a Linux server:

```bash
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o vireo .
```

No cgo, no runtime dependencies.

## Docker

Build the image (multi-stage, distroless, runs as nonroot):

```bash
docker build -t vireo .
```

Run the daemon:

```bash
docker run --rm \
  -p 7777:7777/tcp -p 7777:7777/udp \
  -v "$PWD/config:/app/config:ro" \
  -v "$PWD/sessions:/app/sessions" \
  vireo --config demo
```

Both protocols need publishing: `7777/tcp` for TLS, `7777/udp` for QUIC.

Full demo with the backend in a second container:

```bash
docker network create vireo-demo

docker run -d --name fakeagent --network vireo-demo \
  --entrypoint /app/fakeagent vireo

docker run --rm --network vireo-demo --name vireo \
  -p 7777:7777/tcp -p 7777:7777/udp \
  -v "$PWD/config:/app/config:ro" \
  vireo --config demo
```

Inside the network the backend is reachable as `http://fakeagent:8088/vireo/message`,
so a container config sets `transport` to that instead of `127.0.0.1`.

## Certificates

**Certificate material is never committed.** `certs/` is ignored except for a
placeholder, because a tracked `key.pem` is one `git commit -a` away from being
published. The daemon reads whatever pair you put there, or whatever you mount
over `/app/certs`.

Development, self-signed for `localhost` and `127.0.0.1`:

```bash
./scripts/gen-certs.sh
# wrote certs/cert.pem and certs/key.pem — both ignored by git
```

Production, Let's Encrypt on a hostname pointing at this machine:

```bash
sudo certbot certonly --standalone -d agent.example.com
```

```yaml
server:
  cert_file: "/etc/letsencrypt/live/agent.example.com/fullchain.pem"
  key_file: "/etc/letsencrypt/live/agent.example.com/privkey.pem"
```

In a container, copy the pair into a directory and mount it, so renewals are a
restart rather than a rebuild:

```bash
docker run … -v /srv/vireo-certs:/app/certs:ro vireo --config ziwa
```

The certificate must cover the **domain a client asks for**, which is not
necessarily the SRV target: a client that resolves `_frontdoor._tcp.example.com`
verifies `example.com`, even though it connects to `agent.example.com`. Give
the certificate both names if you also want direct `host:port` addressing to
work:

```bash
sudo certbot certonly --standalone -d example.com -d agent.example.com
```

Renewal reloads the daemon, which re-reads the certificate on start:

```bash
sudo certbot renew --deploy-hook "systemctl restart vireo"
# or, in a container: --deploy-hook "docker restart vireo"
```

TLS 1.3 is required; there is no plaintext mode.

## Install on a server

```bash
sudo useradd --system --home /opt/vireo --shell /usr/sbin/nologin vireo

sudo install -d -o vireo -g vireo /opt/vireo
sudo install -d -o vireo -g vireo /opt/vireo/config
sudo install -d -o vireo -g vireo /opt/vireo/sessions
sudo install -d -o vireo -g vireo /opt/vireo/certs

sudo install -o vireo -g vireo vireo /opt/vireo/
sudo install -o vireo -g vireo config/restaurant.yaml /opt/vireo/config/
sudo install -o vireo -g vireo certs/cert.pem certs/key.pem /opt/vireo/certs/
```

`config/`, `certs/` and `sessions/` resolve relative to the working directory,
so the service must run from `/opt/vireo`.

## Firewall

One port, two protocols:

```bash
sudo ufw allow 7777/tcp
sudo ufw allow 7777/udp
```

There is no HTTP reverse proxy in front — this is raw TLS and QUIC, not HTTP, so
nginx or Caddy cannot terminate it.

## DNS discovery

A peer given a bare domain resolves `_frontdoor._tcp.<domain>`. That record is the
only thing consulted — no port is assumed — so publishing it is what makes
`connect example.com` work:

```
_frontdoor._tcp.example.com. 300 IN SRV 10 5 7777 agent.example.com.
```

Check it:

```bash
dig +short SRV _frontdoor._tcp.example.com
```

## systemd

`/etc/systemd/system/vireo.service`:

```ini
[Unit]
Description=vireo
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/opt/vireo/vireo --config restaurant
WorkingDirectory=/opt/vireo
User=vireo
Group=vireo
Restart=on-failure
RestartSec=2
StateDirectory=vireo
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/vireo/sessions

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now vireo
journalctl -u vireo -f
```

A healthy start looks like:

```
loaded config/restaurant.yaml
agent ready: orders (via https, sessions=true, mint=^[A-Za-z0-9_-]{32}$)
session store: file sessions/vireo-sessions.json — 0 record(s) (0 open, 0 closed, 0 expired)
endpoint "restaurant" (restaurant-prod) fronts 2 agent(s); commands: session.close, session.get, session.list, session.new, session.prune
listening on [::]:7777 (tcp/tls, alpn=frontdoor/1)
listening on [::]:7777 (quic, alpn=frontdoor/1)
```

## Operations

**Sessions** live in `sessions/<file>.json` and survive restarts. Back that file
up if conversation history matters; it holds envelopes only — dates, summaries,
turn counts — never message content.

**Pruning** runs in the background when `session.retention` is set, and can be
triggered on demand over the wire with the `session.prune` operation.

**Upgrade**: replace the binary, restart the service. Session envelopes are read
back on start, so open conversations survive.

**Logs** are line-oriented on stdout; `journalctl -u vireo -f` is enough. Set
nothing — the daemon has no log file of its own.
