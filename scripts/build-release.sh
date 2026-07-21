#!/usr/bin/env bash
# build-release.sh — build, strip, and compress one prebuilt libimbhgo.a for a target cell, ready to
# attach to a GitHub release. Consumers fetch these with cmd/imbhgo-fetch and never build Rust.
#
# The combined libimbhgo.a is architecture-native object code (imbh + sable runtime + our handlers +
# the imbhgo_*/sable_* C ABI). Cross-building the Linux cells uses a cross C compiler (zig cc is
# convenient, matching sable's `make cross`); darwin/windows cells build natively on their runners.
#
# Usage:
#   RUST_TARGET=x86_64-unknown-linux-gnu GOOS=linux GOARCH=amd64 LIBC=glibc \
#     [CROSS_CC="zig cc -target x86_64-linux-gnu"] bash scripts/build-release.sh
#
# Env:
#   RUST_TARGET  required. Rust target triple (e.g. aarch64-unknown-linux-musl).
#   GOOS/GOARCH  required. Used only to name the asset (must match the triple).
#   LIBC         glibc (default) or musl; only affects the Linux asset name.
#   VERSION      release version for the asset name; defaults to internal/release.Version.
#   CROSS_CC     optional cross C compiler *command line* (e.g. "zig cc -target aarch64-linux-musl"),
#                used to build the C dependencies (zstd-sys et al) for RUST_TARGET. Reaches cargo as
#                per-target CC_/CXX_/AR_ vars via shims; see mkshim below for why a shim is required.
#   OUTDIR       output directory; defaults to .agents-workspace/tmp/release.
set -euo pipefail
cd "$(dirname "$0")/.."

: "${RUST_TARGET:?set RUST_TARGET (e.g. x86_64-unknown-linux-gnu)}"
: "${GOOS:?set GOOS}"
: "${GOARCH:?set GOARCH}"
LIBC="${LIBC:-glibc}"
OUTDIR="${OUTDIR:-.agents-workspace/tmp/release}"

# Derive the default VERSION from the single source of truth (internal/release/release.go) so a
# release asset can never drift from the version compiled into the binding.
if [[ -z "${VERSION:-}" ]]; then
  VERSION="$(sed -n 's/^const Version = "\(.*\)"/\1/p' internal/release/release.go | head -n1)"
fi
: "${VERSION:?could not derive VERSION from internal/release/release.go}"

# Asset name mirrors internal/release.AssetName: only Linux musl carries a libc suffix.
suffix=""
if [[ "$GOOS" == "linux" && "$LIBC" == "musl" ]]; then
  suffix="-musl"
fi
ASSET="libimbhgo-${VERSION}-${GOOS}-${GOARCH}${suffix}.a.zst"

echo ">> building libimbhgo.a for ${RUST_TARGET} (${GOOS}/${GOARCH}, ${LIBC})"
rustup target add "$RUST_TARGET" >/dev/null 2>&1 || true

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# Emit an executable shim at $work/bin/$1 that execs "$2 $3 ..." with the caller's args appended.
#
# Two reasons this shim exists, both observed in CI rather than theorised:
#  1. CROSS_CC is a *command line* ("zig cc -target aarch64-linux-musl"), but cc-rs and cargo both
#     want a single executable path. Handed the raw string, cc-rs keeps only the first word and
#     invokes a bare `zig -E ...`, which zig rejects with "unknown command".
#  2. cc-rs then appends its own `--target=<rust triple>`. Zig triples are arch-os-abi, Rust triples
#     are arch-vendor-os-abi, so zig fails to parse it ("unable to parse target query
#     'x86_64-unknown-linux-musl': UnknownOperatingSystem"). The shim drops that flag; the
#     `-target <zig triple>` baked in from CROSS_CC is the authoritative one.
# This is a narrow subset of what cargo-zigbuild does; see JOURNAL if the argument rewriting grows.
mkshim() {
  local path="$work/bin/$1" word
  mkdir -p "$work/bin"
  printf '#!/bin/sh\n_n=$#\n_i=0\nwhile [ $_i -lt $_n ]; do\n  _a="$1"; shift; _i=$((_i+1))\n' >"$path"
  printf '  case "$_a" in --target=*) ;; *) set -- "$@" "$_a";; esac\ndone\nexec' >>"$path"
  for word in "${@:2}"; do printf ' %q' "$word" >>"$path"; done
  printf ' "$@"\n' >>"$path"
  chmod +x "$path"
  printf '%s' "$path"
}

cargo_env=()
if [[ -n "${CROSS_CC:-}" ]]; then
  cc_words=($CROSS_CC) # intentional word splitting: CROSS_CC is a command line, not a path
  cargo_env+=("CC_${RUST_TARGET}=$(mkshim cross-cc "${cc_words[@]}")")
  # zig is also the archiver and the C++ driver. Point cc-rs at those explicitly so it cannot fall
  # back to the host's `ar`/`c++` when a C dependency (zstd-sys et al) builds for a foreign arch.
  if [[ "$(basename "${cc_words[0]}")" == zig ]]; then
    driver="${cc_words[0]}"
    targetflags=(${cc_words[@]+"${cc_words[@]:2}"}) # the "-target <triple>" tail, if any
    cargo_env+=(
      "CXX_${RUST_TARGET}=$(mkshim cross-cxx "$driver" c++ ${targetflags[@]+"${targetflags[@]}"})"
      "AR_${RUST_TARGET}=$(mkshim cross-ar "$driver" ar)"
    )
  fi
fi
# Deliberately no CARGO_TARGET_<triple>_LINKER and no global `export CC`: rust/ is `crate-type =
# ["staticlib"]` with no bins, so cargo never links a target artifact (rustc archives the .a itself)
# — the override bought nothing, and on the cell where the target triple equals the host it hijacked
# *build-script* linking, invoking `zig -m64 ...`. Likewise a global CC would aim the cross compiler
# at host build scripts. Per-target CC_/CXX_/AR_ vars reach exactly the C dependencies that need them.

# `${cargo_env[@]+...}` guard: macOS ships bash 3.2, where an empty array expands as *unset* under
# `set -u`. The darwin cells pass no CROSS_CC, so a bare "${cargo_env[@]}" aborts the build there.
env ${cargo_env[@]+"${cargo_env[@]}"} cargo build --release --manifest-path rust/Cargo.toml --target "$RUST_TARGET"

ARCHIVE="rust/target/${RUST_TARGET}/release/libimbhgo.a"
[[ -f "$ARCHIVE" ]] || { echo "missing built archive: $ARCHIVE" >&2; exit 1; }

mkdir -p "$OUTDIR"
cp -f "$ARCHIVE" "$work/libimbhgo.a"

# Strip to shrink the ~450 MB unstripped archive. Cross builds need the target's strip; fall back to
# the host strip (harmless no-op if it can't process the object, guarded so the build still ships).
STRIP="${STRIP:-strip}"
echo ">> stripping"
"$STRIP" -S "$work/libimbhgo.a" 2>/dev/null || echo "   (strip skipped: $STRIP could not process $RUST_TARGET)"

echo ">> compressing -> ${OUTDIR}/${ASSET}"
rm -f "${OUTDIR}/${ASSET}"
zstd -19 -q -o "${OUTDIR}/${ASSET}" "$work/libimbhgo.a"

# Per-asset checksum + append to the release-wide manifest (coreutils sha256sum format). macOS
# runners have no coreutils, so fall back to `shasum -a 256`, which emits the identical format.
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    shasum -a 256 "$@"
  fi
}
( cd "$OUTDIR" && sha256 "$ASSET" | tee "${ASSET}.sha256" >> SHA256SUMS )

echo ">> done: ${OUTDIR}/${ASSET}"
ls -la "${OUTDIR}/${ASSET}"
