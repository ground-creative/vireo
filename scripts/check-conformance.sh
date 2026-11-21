#!/usr/bin/env bash
# Run the specification's conformance fixtures against this implementation.
#
#   ./scripts/check-conformance.sh [path-to-agent-front-door-spec]
#
# Without an argument it looks for the spec checkout next to this repository,
# which is how the two live in development.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
spec="${1:-}"

if [[ -z "$spec" ]]; then
  for candidate in "$here/../agent-front-door-spec" "$here/../../agent-front-door-spec"; do
    if [[ -d "$candidate/machine/fixtures" ]]; then
      spec="$candidate"
      break
    fi
  done
fi

if [[ -z "$spec" || ! -d "$spec/machine/fixtures" ]]; then
  echo "check-conformance: no specification checkout found." >&2
  echo "  clone https://github.com/ground-creative/agent-front-door-spec and pass its path:" >&2
  echo "  ./scripts/check-conformance.sh /path/to/agent-front-door-spec" >&2
  exit 1
fi

export AGENT_FRONT_DOOR_SPEC="$(cd "$spec" && pwd)"
export PATH="/usr/local/go/bin:$PATH"

echo "conformance fixtures: $AGENT_FRONT_DOOR_SPEC"
cd "$here"
go test -count=1 -run 'TestConformance' ./...
