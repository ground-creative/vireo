#!/usr/bin/env bash
# dev.sh — run a whole local vireo stack with one command.
#
#   ./scripts/dev.sh                     # daemon + client, with the stub backend
#   ./scripts/dev.sh --config openclaw   # the same, plus the gateway and bridge
#   ./scripts/dev.sh --config demo -q    # quieter
#
# What it handles, so a local run needs no decisions:
#
#   * generates certs/ if missing (scripts/gen-certs.sh)
#   * builds the binaries if missing or older than the sources
#   * starts the backend(s) the chosen config needs, and stops them on exit
#   * connects vireo-chat with -insecure, which is correct on loopback: the
#     certificate is there because the transport is TLS-only, not because a
#     local socket has anything to defend against. See docs/LOCAL-TESTING.md.
#
# Every child's output is prefixed with a tag, so a failure is attributable at a
# glance. Ctrl-C stops everything; no orphans are left holding a port.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

config="demo"
quiet=""
while [ $# -gt 0 ]; do
  case "$1" in
    -c|--config) config="${2:-}"; shift 2 ;;
    -q|--quiet)  quiet="1"; shift ;;
    -h|--help)   sed -n '2,17p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "dev.sh: unknown argument $1 (try --help)" >&2; exit 2 ;;
  esac
done

log()  { [ -n "$quiet" ] || printf '\033[1;36m▸\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; exit 1; }

# --- locate the config -------------------------------------------------------

# --config takes a name inside config/, which is the daemon's own rule.
cfg_name="$(basename "${config%.yaml}")"
cfg_path="config/$cfg_name.yaml"
[ -f "$cfg_path" ] || die "no config at $cfg_path — try: $0 --config demo"

# --- prerequisites ----------------------------------------------------------

command -v go >/dev/null || die "go is not on PATH; install Go 1.22+ or add it to PATH"

# --- certificate -------------------------------------------------------------

# Not optional: the daemon is TLS-only and reads cert_file/key_file. Generated
# once and reused; delete the pair to have the next run recreate it.
if [ ! -f certs/cert.pem ] || [ ! -f certs/key.pem ]; then
  log "no certificate found — generating one for localhost"
  ./scripts/gen-certs.sh >/dev/null
fi

# --- binaries ----------------------------------------------------------------

build_if_stale() {
  local out="$1" pkg="$2"
  if [ ! -x "$out" ] || [ -n "$(find . -name '*.go' -newer "$out" -print -quit 2>/dev/null)" ]; then
    log "building $out"
    go build -o "$out" "$pkg" || die "build failed for $out"
  fi
}

build_if_stale vireo .
build_if_stale vireo-chat ./cmd/vireo-chat

# --- child process management ------------------------------------------------

# Job control gives each background job its own process group, so cleanup can
# kill a whole pipeline (`{ cmd | prefix; } &`) rather than only its subshell.
set -m
pids=()
cleanup() {
  local pid
  for pid in "${pids[@]:-}"; do
    [ -n "$pid" ] || continue
    kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
  done
  pids=()
  wait 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# prefix tags each output line so several processes in one terminal stay readable.
prefix() {
  local tag="$1"
  while IFS= read -r line; do
    printf '\033[2m[%s]\033[0m %s\n' "$tag" "$line"
  done
}
start_child() {
  local tag="$1"; shift
  # No pipeline: `cmd | prefix` runs cmd in a subshell, and killing the subshell
  # leaves cmd alive holding its port. Letting the kernel own the process group
  # (set -m above) means cleanup's `kill -- -PGID` reaches every descendant,
  # including any grandchild a backend spawns.
  "$@" > >(prefix "$tag" >&2) 2>&1 &
  pids+=($!)
}

# wait_http polls a health endpoint; on timeout the child's output is already
# above the failure, which is what makes it diagnosable.
wait_http() {
  local url="$1" what="$2" tries="${3:-80}"
  local i
  for ((i = 0; i < tries; i++)); do
    curl -fsS -m 1 "$url" >/dev/null 2>&1 && return 0
    sleep 0.25
  done
  die "$what did not become healthy at $url (its output is above)"
}

# --- the backend(s) this config needs ---------------------------------------

case "$cfg_name" in
  demo)
    log "starting the stub backend (fakeagent on :8088)"
    build_if_stale fakeagent ./cmd/fakeagent
    start_child fakeagent ./fakeagent
    wait_http http://127.0.0.1:8088/health "fakeagent"
    ;;

  openclaw)
    if [ -z "${OPENCLAW_GATEWAY_TOKEN:-}" ] && [ -r "$HOME/.openclaw/openclaw.json" ]; then
      OPENCLAW_GATEWAY_TOKEN="$(jq -r '.gateway.auth.token // empty' "$HOME/.openclaw/openclaw.json" 2>/dev/null || true)"
    fi
    [ -n "${OPENCLAW_GATEWAY_TOKEN:-}" ] || die "OPENCLAW_GATEWAY_TOKEN is not set and could not be read from ~/.openclaw/openclaw.json"
    export OPENCLAW_GATEWAY_TOKEN

    # The gateway must be up first: the bridge probes it at startup and refuses
    # to start against a dead endpoint.
    if curl -fsS -m 2 http://127.0.0.1:18789/health >/dev/null 2>&1; then
      log "using the OpenClaw gateway already running on :18789"
    else
      oc_bin="$(command -v openclaw || true)"
      if [ -z "$oc_bin" ]; then
        for cand in "$HOME"/.npm/_npx/*/node_modules/.bin/openclaw; do
          [ -x "$cand" ] && { oc_bin="$cand"; break; }
        done
      fi
      [ -n "$oc_bin" ] || die "gateway not running and no openclaw CLI found; put node 24 and the npx .bin dir on PATH, then rerun"
      log "starting the OpenClaw gateway (:18789)"
      start_child gateway "$oc_bin" gateway run
      wait_http http://127.0.0.1:18789/health "the gateway" 160
    fi

    log "starting the OpenClaw bridge (:18090)"
    build_if_stale openclaw-bridge ./cmd/openclaw-bridge
    bridge_cfg=openclaw-bridge.yaml
    [ -f "$bridge_cfg" ] || bridge_cfg=openclaw-bridge.example.yaml
    start_child bridge ./openclaw-bridge -config "$bridge_cfg"
    wait_http http://127.0.0.1:18090/health "the bridge"
    ;;

  *)
    [ -n "$quiet" ] || log "config '$cfg_name': no known backend to start — launching the daemon and client only"
    ;;
esac

# --- daemon ------------------------------------------------------------------

port="$(awk '/^server:/{s=1} s&&/^[[:space:]]+port:/{print $2; exit}' "$cfg_path" 2>/dev/null || true)"
port="${port:-7777}"

log "starting the daemon (vireo --config $cfg_name, port $port)"
start_child vireo ./vireo --config "$cfg_name"

# The daemon opens TCP and QUIC on one port; wait for TCP so the client does not
# race the listener.
for ((i = 0; i < 80; i++)); do
  (exec 3<>"/dev/tcp/127.0.0.1/$port") 2>/dev/null && break
  sleep 0.25
done

# --- client ------------------------------------------------------------------

log "connecting vireo-chat — Ctrl-C stops every process above"
echo

# -insecure is deliberate: a loopback socket to a daemon started moments ago.
# The certificate exists because the transport is TLS-only, not because there is
# an attacker on a local socket. Use -ca (or a public certificate) when the same
# config may later point at a real deployment.
#
# Run the client as a normal child, not with `exec`: exec would replace this
# shell, so the EXIT trap would never fire and the backend and daemon would
# outlive the client. Interrupting the client is the normal way to stop the
# stack, so that exit path is the one that has to work.
if [ -n "${VIREO_CLIENT_ID:-}" ]; then
  ./vireo-chat -insecure -id "$VIREO_CLIENT_ID" "localhost:$port"
else
  ./vireo-chat -insecure "localhost:$port"
fi
