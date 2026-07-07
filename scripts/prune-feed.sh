#!/bin/sh
# Prune old VERSIONED feed artefacts, keeping the newest $KEEP builds per family. Generic over
# rave-mate AND rave-app - pass the family prefixes via PREFIXES (space-separated); defaults to
# the rave-mate set. Run over SSH from a deploy job:
#   ssh host "FEED_DIR=… KEEP=… PREFIXES='rave-app- rave-page-setup-' sh -s" < scripts/prune-feed.sh
# Versioned names are <prefix><build>-<date>-<commit>[.exe]; the monotonic <build> is the sort
# key. Stable fallbacks (e.g. rave-mate.exe, rave-page-setup.exe) and latest.json* never match
# the -<build>- pattern, so stay intact.
set -eu
: "${FEED_DIR:?FEED_DIR required}"
KEEP="${KEEP:-10}"
# Default families = rave-mate (back-compat); rave-app deploy overrides with its own set.
PREFIXES="${PREFIXES:-rave-mate- rave-mate-setup- rave-mate-linux-}"

# prune_family <filename-prefix> - keep newest $KEEP, delete the rest of that family.
prune_family() {
  prefix="$1"
  ls -1 "$FEED_DIR" 2>/dev/null | grep -E "^${prefix}[0-9]+-" | while IFS= read -r f; do
    b="${f#"$prefix"}"        # strip prefix → <build>-<date>-<commit>[.exe]
    b="${b%%-*}"              # first field = build number (numeric sort key)
    printf '%s %s\n' "$b" "$f"
  done | sort -rn | awk -v k="$KEEP" 'NR>k{print $2}' | while IFS= read -r old; do
    rm -f "$FEED_DIR/$old" && echo "pruned $old"
  done
}

# Each prefix + a digit isolates a family (a longer prefix like -setup-/-linux- starts with a
# letter, so the bare prefix's "[0-9]+-" pattern skips it - they're pruned by their own prefix).
for p in $PREFIXES; do
  prune_family "$p"
done
echo "prune: kept newest $KEEP build(s) per family in $FEED_DIR"
