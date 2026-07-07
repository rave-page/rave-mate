#!/usr/bin/env bash
# Supply-chain soak gate - fail if any go.mod dependency is < 7 days old.
# Mirrors the web repo's pnpm `minimum-release-age=10080`. See SUPPLY_CHAIN.md.
set -euo pipefail

MAX_AGE_DAYS=7
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ALLOW="$ROOT/.modage-allow"
now=$(date +%s)
fail=0

allowed() { # module version -> 0 if exempt
  [ -f "$ALLOW" ] || return 1
  grep -qE "^\s*$1\s+$2(\s|$)" "$ALLOW"
}

check_module() { # dir - soak every require in that module's graph
  local dir="$1"
  while read -r mod ver; do
    [ -z "${ver:-}" ] && continue
    t=$( (cd "$dir" && go list -m -json "$mod@$ver" 2>/dev/null) | sed -n 's/.*"Time": "\([^"]*\)".*/\1/p' | head -1)
    [ -z "$t" ] && continue   # pseudo/replaced/local - no proxy time
    epoch=$(date -d "$t" +%s 2>/dev/null || date -j -f "%Y-%m-%dT%H:%M:%SZ" "$t" +%s 2>/dev/null || echo 0)
    [ "$epoch" = 0 ] && continue
    age_days=$(( (now - epoch) / 86400 ))
    if [ "$age_days" -lt "$MAX_AGE_DAYS" ]; then
      if allowed "$mod" "$ver"; then
        echo "ALLOW  $mod $ver (${age_days}d) - exempt via .modage-allow"
      else
        echo "REJECT $mod $ver - ${age_days}d old (< ${MAX_AGE_DAYS}d soak)"
        fail=1
      fi
    fi
  done < <( (cd "$dir" && go list -m -f '{{.Path}} {{.Version}}' all 2>/dev/null) | grep -v '^rave.page/mate')
}

# App module (shipped) + the build-time codegen tool module.
check_module "$ROOT"
[ -f "$ROOT/tools/genapi/go.mod" ] && check_module "$ROOT/tools/genapi"

if [ "$fail" -ne 0 ]; then
  echo "Supply-chain gate FAILED - see SUPPLY_CHAIN.md" >&2
  exit 1
fi
echo "Supply-chain gate passed (soak ${MAX_AGE_DAYS}d)."
