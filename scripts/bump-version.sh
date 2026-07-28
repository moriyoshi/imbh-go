#!/usr/bin/env bash
# bump-version.sh — rewrite the release version across the tree, ahead of cutting a tag.
#
# Only one occurrence is load-bearing: `const Version` in internal/release/release.go. The release
# workflow refuses to publish when the pushed tag disagrees with it (see the "Resolve version" step
# in .github/workflows/release.yml). Everything else this touches is documentation — the README
# quickstart and the cmd/imbhgo-fetch doc comment / flag help — which silently rots if a bump misses
# it, which is exactly what this script exists to prevent.
#
# This only edits files. Committing, tagging, and pushing stay manual: publishing is triggered by the
# tag push, so that step is the point of no return and belongs to a human.
#
# Usage:
#   bash scripts/bump-version.sh v0.1.2
#   VERSION=v0.1.2 bash scripts/bump-version.sh
#
# Env:
#   VERSION  the new version, if not passed as $1. Must be vMAJOR.MINOR.PATCH[-prerelease].
#   FORCE    set to 1 to proceed even when the target tag already exists locally.
set -euo pipefail
cd "$(dirname "$0")/.."

# The full set of files carrying the version. Deliberately excludes .agents/docs/** — JOURNAL.md and
# TODO.md refer to past releases as history, and rewriting those would falsify the record.
FILES=(
  internal/release/release.go
  cmd/imbhgo-fetch/main.go
  README.md
)

VERSION_RE='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

new="${1:-${VERSION:-}}"
if [ -z "$new" ]; then
  echo "usage: bash scripts/bump-version.sh vMAJOR.MINOR.PATCH (or VERSION=... )" >&2
  exit 2
fi
if ! [[ "$new" =~ $VERSION_RE ]]; then
  echo "error: '$new' is not a vMAJOR.MINOR.PATCH[-prerelease] version" >&2
  exit 2
fi

old="$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/release/release.go | head -n1)"
if [ -z "$old" ]; then
  echo "error: could not read 'const Version' from internal/release/release.go" >&2
  exit 1
fi
if [ "$old" = "$new" ]; then
  echo "already at $new; nothing to do"
  exit 0
fi

# A tag that already exists means this version was (or is being) published; rewriting the tree to
# match it would produce a second, different commit claiming to be the same release.
if git rev-parse -q --verify "refs/tags/$new" >/dev/null; then
  if [ "${FORCE:-}" != "1" ]; then
    echo "error: tag $new already exists. Re-run with FORCE=1 only if you intend to retag." >&2
    exit 1
  fi
  echo "warning: tag $new already exists; continuing because FORCE=1" >&2
fi

# Escape the regex metacharacter that can appear in a version ('.'); the rest of the charset is
# literal in a POSIX BRE.
esc_old="${old//./\\.}"

echo "bumping $old -> $new"
for f in "${FILES[@]}"; do
  [ -f "$f" ] || { echo "error: missing $f" >&2; exit 1; }
  n="$(grep -c -- "$esc_old" "$f" || true)"
  if [ "$n" -eq 0 ]; then
    echo "  $f: no occurrences (check whether it should still carry the version)" >&2
    continue
  fi
  # sed -i is not portable across GNU/BSD; write beside the file and move it into place.
  sed "s/$esc_old/$new/g" "$f" >"$f.bump.tmp"
  # Overwriting via mv preserves nothing of the original, so only do it once the rewrite succeeded.
  mv -f "$f.bump.tmp" "$f"
  echo "  $f: $n line(s)"
done

# The one that actually gates CI — assert it rather than trusting the sweep.
if ! grep -q "^const Version = \"$new\"\$" internal/release/release.go; then
  echo "error: internal/release/release.go does not declare Version = \"$new\" after the rewrite" >&2
  exit 1
fi

# Nothing here reformats Go code, but a stray edit that does would break the build gate later.
if command -v gofmt >/dev/null 2>&1; then
  go_files=()
  for f in "${FILES[@]}"; do
    case "$f" in *.go) go_files+=("$f");; esac
  done
  if [ ${#go_files[@]} -gt 0 ]; then
    unformatted="$(gofmt -l "${go_files[@]}")"
    if [ -n "$unformatted" ]; then
      echo "error: gofmt reports unformatted files:" >&2
      echo "$unformatted" >&2
      exit 1
    fi
  fi
fi

echo "done. Review with: git diff -- ${FILES[*]}"
