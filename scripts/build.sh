#!/usr/bin/env bash
# build.sh — build every binary in this repository.
#
#   ./scripts/build.sh              # → ./bin
#   ./scripts/build.sh /usr/local/bin
#
# The set is discovered, not listed: every directory under cmd/ holding a main
# package becomes a binary with that directory's name, and the root package
# becomes `vireo`. Adding a backend is a new cmd/<name>/ directory and nothing
# else — no build file, README, or Dockerfile needs to change.
#
#   cmd/vireo-chat        → bin/vireo-chat
#   cmd/fakeagent         → bin/fakeagent
#   bridges/openclaw-bridge   → bin/openclaw-bridge
#   . (package main)      → bin/vireo
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

out="${1:-bin}"
mkdir -p "$out"

command -v go >/dev/null || { echo "build.sh: go is not on PATH" >&2; exit 1; }

built=()
failed=()

# The daemon is the root package. Its name is not derived from a directory, so
# it is the single entry that is stated rather than discovered.
build() {
  local name="$1" pkg="$2"
  if go build -o "$out/$name" "$pkg"; then
    built+=("$out/$name")
  else
    failed+=("$name")
  fi
}

# cmd/<name> and bridges/<name> — discovered. A directory without a main package
# is skipped silently: it is not a binary, and saying so on every run is noise.
for dir in cmd/*/ bridges/*/; do
  [ -d "$dir" ] || continue
  name="$(basename "$dir")"
  if go list -f '{{.Name}}' "./$dir" 2>/dev/null | grep -qx main; then
    build "$name" "./$dir"
  fi
done

# The root package, when it is a main package (it is).
if go list -f '{{.Name}}' . 2>/dev/null | grep -qx main; then
  build vireo .
fi

if [ ${#failed[@]} -gt 0 ]; then
  echo "build.sh: failed: ${failed[*]}" >&2
  exit 1
fi

if [ ${#built[@]} -eq 0 ]; then
  echo "build.sh: nothing to build" >&2
  exit 1
fi

printf '%s\n' "${built[@]}"
