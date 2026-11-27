#!/usr/bin/env bash
# dev.sh — run a whole local vireo stack with one command.
#
#   ./scripts/dev.sh                     # daemon + client, with the stub backend
#   ./scripts/dev.sh --backend openclaw  # the same, plus the gateway and bridge
#   ./scripts/dev.sh --backend openclaw -q
#
# There is one YAML in config/, demo.yaml. Any other backend gets a scratch config
# written to config/dev-<backend>.yaml and deleted on exit, so the repository does
# not carry a shipped file per backend.
#
# What it handles, so a local run needs no decisions:
#
#   * generates certs/ if missing (scripts/gen-certs.sh)
#   * builds the binaries (scripts/build.sh) and starts what the backend needs
#   * connects vireo-chat with -insecure, which is correct on loopback: the
#     certificate is there because the transport is TLS-only. See
#     docs/LOCAL-TESTING.md
#
# Every child's output is prefixed with a tag, so a failure is attributable at a
# glance. Ctrl-C stops everything; no orphans are left holding a port.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

backend=""
config=""
quiet=""
while [ $# -gt 0 ]; do
  case "$1" in
    -b|--backend) backend="${2:-}"; shift 2 ;;
    -c|--config)  config="${2:-}"; shift 2 ;;
    -q|--quiet)   quiet="1"; shift ;;
    -h|--help)    sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "dev.sh: unknown argument $1 (try --help)" >&2; exit 2 ;;
  esac
done

log()  { [ -n "$quiet" ] || printf '\033[1;36m▸\033[0m %s\n' "$*"; }
die()  { printf '\033[1;31m✗\033[0m %s\n' "$*" >&2; exit 1; }

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

# One build step for the whole repository, so a backend added to cmd/ is built
# without this script knowing its name. build.sh is incremental enough for the
# loop: it rebuilds everything, which for this repo is a couple of seconds.
BIN=bin
mkdir -p "$BIN"
log "building binaries into $BIN/"
./scripts/build.sh "$BIN" >/dev/null || die "build failed (run ./scripts/build.sh to see why)"

# --- child process management ------------------------------------------------

# Job control gives each background job its own process group, so cleanup can
# kill a whole pipeline (`{ cmd | prefix; } &`) rather than only its subshell.
set -m
pids=()
scratch_cfg=""   # set when this run wrote config/dev-<backend>.yaml
cleanup_cfg() {
  [ -n "$scratch_cfg" ] && rm -f "$scratch_cfg"
  return 0
}
cleanup() {
  local pid
  for pid in "${pids[@]:-}"; do
    [ -n "$pid" ] || continue
    kill -- "-$pid" 2>/dev/null || kill "$pid" 2>/dev/null || true
  done
  pids=()
  wait 2>/dev/null || true
}
# The one EXIT handler: stop every child in its process group, then remove the
# scratch config if this run created one. Nothing else may install a trap.
trap 'cleanup; cleanup_cfg' EXIT INT TERM

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

# --- the config, and the backend it needs -----------------------------------

# There is one committed config: config/demo.yaml. A backend that needs a
# different endpoint gets a scratch config written here and deleted on exit,
# because a shipped file per backend would be demo.yaml with a different port and
# transport — maintained, ignored, and explained again for no gain. The shape
# lives in demo.yaml; the differences live in cmd/<backend>/README.md.

if [ -n "$config" ]; then
  # An explicit --config wins, for someone keeping their own endpoint.
  cfg_name="$(basename "${config%.yaml}")"
  cfg_path="config/$cfg_name.yaml"
  [ -f "$cfg_path" ] || die "no config at $cfg_path"
else
  case "${backend:-demo}" in
    demo)
      cfg_name=demo
      cfg_path=config/demo.yaml
      ;;
    openclaw)
      cfg_name=dev-openclaw
      cfg_path="config/$cfg_name.yaml"
      scratch_cfg="$cfg_path"
      # Do NOT trap here: cleanup (installed below) stops the children and is the
      # one EXIT handler. Trapping again would silently replace it and leave the
      # daemon and bridge running after the client exits.
      mkdir -p config   # the daemon reads YAML only from config/
      cat > "$cfg_path" <<'YAML'
# Generated by scripts/dev.sh for a local run. Not committed; deleted on exit.
# Same shape as demo.yaml, with the OpenClaw bridge as the backend.
server:
  port: 7779
  cert_file: "certs/cert.pem"
  key_file: "certs/key.pem"
  alpn:
    - "portico/1"
  call_timeout: "200s"        # above the bridge's 180s gateway timeout
  rate_limit:
    enabled: true
    requests_per_minute: 60
    burst: 10
  endpoint:
    id: "openclaw-dev"
    name: "openclaw"
    description: "A local OpenClaw agent, reachable over the portico protocol."

transport: "http://127.0.0.1:18090/vireo/message"

session:
  list: true
  ttl_default: 3600
  store: "file"
  file: "dev-openclaw-sessions.json"
  retention: "720h"

agents:
  - name: "concierge"
    description: "Ask the local OpenClaw agent anything; it keeps a conversation."
    start_message: "Hi — I'm the OpenClaw agent. What can I help with?"
    timeout: "190s"
YAML
      ;;
    *)
      die "unknown --backend '$backend' (try: demo, openclaw — or --config <name>)"
      ;;
  esac
fi

# --- the backend process(es) -------------------------------------------------

case "${backend:-}" in
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
    # A local override wins; otherwise the example next to the command is used,
    # so a first run needs no copy step.
    bridge_cfg=openclaw-bridge.yaml
    if [ ! -f "$bridge_cfg" ]; then
      bridge_cfg=bridges/openclaw-bridge/openclaw-bridge.example.yaml
      [ -n "$quiet" ] || log "no openclaw-bridge.yaml — using the shipped example"
    fi
    start_child bridge "$BIN/openclaw-bridge" -config "$bridge_cfg"
    wait_http http://127.0.0.1:18090/health "the bridge"
    ;;

  "")
    # demo.yaml names an agent served by fakeagent; start it.
    if [ "$cfg_name" = "demo" ]; then
      log "starting the stub backend (fakeagent on :8088)"
      start_child fakeagent "$BIN/fakeagent"
      wait_http http://127.0.0.1:8088/health "fakeagent"
    else
      [ -n "$quiet" ] || log "config '$cfg_name': no backend to start"
    fi
    ;;
esac

# --- daemon ------------------------------------------------------------------

port="$(awk '/^server:/{s=1} s&&/^[[:space:]]+port:/{print $2; exit}' "$cfg_path" 2>/dev/null || true)"
port="${port:-7777}"

log "starting the daemon (vireo --config $cfg_name, port $port)"
start_child vireo "$BIN/vireo" --config "$cfg_name"

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
  "$BIN/vireo-chat" -insecure -id "$VIREO_CLIENT_ID" "localhost:$port"
else
  "$BIN/vireo-chat" -insecure "localhost:$port"
fi
