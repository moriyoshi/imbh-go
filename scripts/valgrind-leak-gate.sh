#!/usr/bin/env bash
# valgrind-leak-gate.sh — prove the Arrow *buffers* (and FfiBatch shells) are freed, not just the
# shell counter. Runs the binding under Valgrind and asserts ZERO definite-loss blocks allocated via
# libc malloc.
#
# Why this filter: Go's GC-managed heap (runtime.mallocgc / valgrindClientRequest) shows up as
# "definitely lost" under Valgrind because its leak checker cannot trace Go's GC roots — these are
# false positives, even with Go's `-tags valgrind` runtime instrumentation (CL 674077). Rust's global
# allocator, by contrast, uses libc malloc/free, which Valgrind traces accurately. So a real leak of a
# Rust-allocated Arrow buffer or an FfiBatch shell WOULD appear as a definite-loss block whose
# allocation stack goes through `malloc` (not `mallocgc`). We count exactly those; the gate passes iff
# there are none.
#
# Requires: valgrind, a built rust/target/release/libimbhgo.a. Uses `-tags sable_safe` so Valgrind
# isn't tripped by sable's asm/g0 fast path (plain cgo crossing instead); `-tags valgrind` enables the
# Go runtime's Valgrind annotations.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v valgrind >/dev/null || { echo "valgrind not installed"; exit 2; }

OUT="${TMPDIR:-/tmp}/imbhgo-vg"
mkdir -p "$OUT"
BIN="$OUT/imbhgo.test"

echo ">> building test binary (-tags 'sable_extern_lib sable_safe valgrind')"
go test -c -tags "sable_extern_lib sable_safe valgrind" -o "$BIN" ./

# Tests that allocate + free Arrow buffers on both ownership paths: full-drain (taken → shell_free +
# Go Record.Release) and abandoned (many buffered batches → Rust batch_release).
TESTS='^(TestIngestAndQuery|TestQueryIngestedBodies|TestAbandonedBatchesFreed)$'

echo ">> running under valgrind (GOGC=off; this is slow)"
log="$OUT/valgrind.log"
GOGC=off GODEBUG=asyncpreemptoff=1 valgrind \
  --leak-check=full --show-leak-kinds=definite --error-exitcode=0 --num-callers=30 \
  "$BIN" -test.run "$TESTS" >"$log" 2>&1 || true

grep -qE "^(ok|PASS)|--- PASS" "$log" || { echo "!! tests did not pass under valgrind"; tail -20 "$log"; exit 1; }

# Count definite-loss records whose allocation stack goes through libc malloc but NOT Go's allocator.
nonjunk=$(awk '
  /definitely lost in loss record/ {inblk=1; hasm=0; hasg=0; buf=""}
  inblk {buf=buf $0 "\n"
    if ($0 ~ /vg_replace_malloc|: malloc| calloc| realloc/) hasm=1
    if ($0 ~ /mallocgc|valgrindClientRequest|newobject|makeslice|growslice|makemap|newarray|newTable/) hasg=1
  }
  inblk && /^==[0-9]+== *$/ {inblk=0; if (hasm && !hasg) {print buf; n++}}
  END {print "COUNT=" n+0 > "/dev/stderr"}
' "$log" 2>"$OUT/count.txt")

count=$(sed -n 's/COUNT=//p' "$OUT/count.txt")
echo ">> valgrind LEAK SUMMARY (Go-heap losses are expected false positives):"
grep -A5 "LEAK SUMMARY" "$log" | sed 's/^/   /'

if [ "${count:-0}" -eq 0 ]; then
  echo ">> PASS: 0 definite-loss blocks allocated via libc malloc — no Rust/Arrow buffer or shell leak."
  exit 0
else
  echo "!! FAIL: $count libc-malloc definite-loss block(s) — a real Rust-side leak:"
  echo "$nonjunk"
  exit 1
fi
