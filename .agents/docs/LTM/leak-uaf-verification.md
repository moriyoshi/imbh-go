# Leak / UAF Verification: the two gates

## Summary

The two-free ownership protocol ([[zero-copy-arrow-handoff]]) is verified leak-clean two independent ways: an **in-process live-batch counter** that proves the shells and registries balance (no leak, no double-free), and a **Valgrind gate** that proves the Arrow *buffers* themselves are freed. Together they cover both the `FfiBatch` shell and the underlying Arrow memory, across both the taken and abandoned free paths.

## Key Facts

- **In-process counter** — `LIVE_BATCHES: AtomicI64`, incremented in `export_batch` (after `Box::into_raw`), decremented in **both** free paths. Returns to 0 once cursors quiesce; positive residual = leaked shell, negative = double free. Exposed via `imbhgo_live_batches` (+ `imbhgo_live_dbs`, `imbhgo_pending_query_errors` for the registries). Go accessors in `debug.go`.
- **Valgrind gate** — a whole-binary (Go + Rust staticlib) Valgrind run, enabled by Go's `-tags valgrind` runtime instrumentation (go-review **CL 674077**, merged May 2025, present in go1.26.4).
- The Valgrind signal that matters is **libc-malloc-only definite losses = 0** — Rust's global allocator uses libc `malloc`/`free`, which Valgrind traces accurately; Go-heap losses are false positives.
- Both gates are green across the taken (full-drain → `shell_free` + Go `Record.Release`) and abandoned (`batch_release`) paths, in both light and heavy (10 000-row / many-batch) runs.

## Details

### The in-process gate (`leak_test.go`, run under `-race`)

- `TestNoLeak` — 150 mixed cycles (open → ingest → full-drain query [taken] → close-without-draining [abandoned] → failing query [error registry] → close). Asserts, with an `eventually` poll for async frees: live batches back to 0, pending query errors 0, live dbs 0, goroutines ≤ baseline+4, fds ≤ baseline+8.
- `TestAbandonedBatchesFreed` — deterministically exercises `batch_release`: ingest 10 000 rows (> 2× the ~4096 batch size → multiple batches), pull one, `Close`, assert the buffered/abandoned batches are freed.
- **Scope honesty:** this proves the shell and registries balance and the two paths don't double-free. It does NOT by itself prove the Arrow *buffers* are freed — that free is driven by arrow-go's `Record.Release()`. Hence the Valgrind gate.

### The Valgrind buffer gate (`scripts/valgrind-leak-gate.sh`, `make leak-valgrind`)

Two build-tag findings were required to make this work:

1. **`-tags sable_safe` is mandatory.** With the default asm fast path, Valgrind drowns in "invalid read below stack pointer" errors from sable's `asmcgocall` / g0-stack / `morestack` tricks (the test still PASSES functionally, but the noise is unusable). Building with `-tags sable_safe` (plain cgo crossing) eliminates that storm.
2. **`-tags valgrind` fixes memcheck noise but NOT leak-check.** Valgrind still cannot trace Go's GC roots, so all Go-heap allocations show as "definitely lost" at exit (~178 KB light, ~370 KB heavy — `runtime.mallocgc`, stdlib/deps init caches, arrow-go's Go-side `cimporter.importBuffer` wrappers).

**The rigorous filter:** an awk filter keeps only definite-loss blocks whose alloc stack goes through libc `malloc` (not `mallocgc`). A leaked Rust Arrow buffer or `FfiBatch` shell would appear exactly there. Result: **0 records** in both light and heavy runs, across both free paths. The differential confirms it — Go-heap false positives grow with work, but the libc-malloc leak count stays 0.

## Files

- `rust/src/lib.rs` — `LIVE_BATCHES`, `imbhgo_live_batches`, `imbhgo_live_dbs`, `imbhgo_pending_query_errors`.
- `debug.go` — Go accessors (cgo, so not in `_test.go`).
- `leak_test.go` — `TestNoLeak`, `TestAbandonedBatchesFreed`.
- `scripts/valgrind-leak-gate.sh` + `make leak-valgrind` — the build tags and the libc-malloc-only awk filter.

## Test Coverage

Run the counter gate with the normal suite (`go test -tags sable_extern_lib -race ./...`). Run the buffer gate with `make leak-valgrind` (passes iff 0 real Rust-side leaks). Env: valgrind 3.22.0, arm64.

## Pitfalls

- Forgetting `-tags sable_safe` makes the Valgrind run appear broken (memcheck flood) though the protocol is fine.
- Reading Valgrind's raw "definitely lost" total as the leak count is wrong — it is dominated by Go-heap false positives; only the libc-malloc-filtered count is the real signal.
- A pending ASAN/LSan gate is still on the wishlist as a third, sanitizer-based check; design new FFI tests so they can run under a sanitizer.
