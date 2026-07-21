# FFI Ownership & Memory Safety (synthesis)

## Summary

Query results cross the FFI boundary zero-copy, one Arrow record batch at a time. That performance win is bought with manual memory ownership, and this synthesis is the single place to understand it: how a batch is handed over and freed (the two-free protocol), the aliasing rules that make the handover safe on the Go side, and the two independent gates that prove the whole thing neither leaks nor double-frees. These three concerns are inseparable — the protocol is only correct *because* of arrow-go's move-on-import semantics, and it is only *known* correct because of the gates.

## Included Documents

| Document | Focus |
|----------|-------|
| [zero-copy-arrow-handoff.md](./zero-copy-arrow-handoff.md) | The `FfiBatch` handoff and the two-free ownership protocol |
| [arrow-buffer-lifetime-rules.md](./arrow-buffer-lifetime-rules.md) | Move-on-import + `String.Value` aliasing → the `strings.Clone` rule |
| [leak-uaf-verification.md](./leak-uaf-verification.md) | The in-process `LIVE_BATCHES` counter gate + the Valgrind buffer gate |

## Stable Knowledge

### The handoff

- **Zero-copy = Arrow C Data Interface, not IPC.** Each batch crosses as an `FfiBatch { array: FFI_ArrowArray, schema: FFI_ArrowSchema }`, boxed and passed as `Box::into_raw(..) as u64` on sable's `Payload::Handle(sable::Handle)` (a tuple variant; construct via `Handle::new(ptr, release)`).
- **A pointer is a `u64`** — sable's completion already delivers one, so handle payloads needed no new transport.

### The two-free protocol (the core invariant)

Every exported batch is freed exactly once, via exactly one of two paths:

- **Taken path** — Go imported the batch via `cdata.ImportCRecordBatch`, then calls `imbhgo_shell_free`, which frees only the `FfiBatch` box. The Arrow buffers are freed later by arrow-go's `Record.Release()`.
- **Abandoned path** — Go never imported (early `Close`, buffered-but-undrained batches, shutdown with an open cursor), then calls `imbhgo_batch_release`, which fully drops the `FfiBatch` including its still-live inner `release` callbacks (freeing the Arrow buffers too).

### Why move semantics make this correct

- **arrow-go's `cdata` import is a zero-copy MOVE** that **nulls the source's `release`** on import. After a successful import the returned `RecordBatch.Release()` is the sole owner. That is exactly why the taken path (`shell_free`) must *forget* the inner array — trying to release it again would double-free — while the abandoned path (`batch_release`) *does* still own it.

### The Go-side aliasing hazard

- **arrow-go's `String.Value(i)` ALIASES the value buffer** (unsafe, zero-copy). Any string or `[]byte` kept past `rec.Release()` is a use-after-free. **Rule: `strings.Clone` every string, copy every `[]byte`, on copy-out.** This is a general caller rule, not decoder-specific; the typed-query decoders do it for you.

### The two verification gates

- **In-process counter** — `LIVE_BATCHES: AtomicI64`, incremented in `export_batch` (after `Box::into_raw`), decremented in **both** free paths. Returns to 0 once cursors quiesce; positive = leaked shell, negative = double free. Proves shell + registry balance.
- **Valgrind buffer gate** — proves the Arrow *buffers* (not just the shells) are freed. The real signal is **libc-malloc-only definite losses = 0** (Rust's allocator uses libc `malloc`; Go-heap losses are false positives Valgrind can't trace).

## Operational Guidance

- Touching batch handoff, handle lifetime, or stream cancellation? The load-bearing tests are the leak/UAF gates — extend `TestNoLeak`, `TestAbandonedBatchesFreed`, and the Valgrind gate rather than only adding functional tests.
- Any new code that reads Arrow strings/bytes and outlives the batch must copy-out first — audit for a missing `strings.Clone` when strings come back garbled (short survive, long corrupt is the tell).
- When adding a free path or a new exported-object type, wire it into `LIVE_BATCHES` (or an analogous counter) so the balance stays checkable.

## Files

- `rust/src/lib.rs` — `FfiBatch`, `export_batch`, `imbhgo_shell_free` (taken), `imbhgo_batch_release` (abandoned), `LIVE_BATCHES` + `imbhgo_live_batches`/`imbhgo_live_dbs`/`imbhgo_pending_query_errors`.
- `db.go` — `Rows` import path (`cdata.ImportCRecordBatch`) + the two free shims.
- `results.go` — decoders apply the `strings.Clone` copy-out.
- `debug.go` — Go accessors for the counters (cgo, so not in a `_test.go`).
- `imbhgo.h` — C ABI for the free shims + field-address helpers.
- `scripts/valgrind-leak-gate.sh`, `make leak-valgrind` — the Valgrind build tags + libc-malloc-only awk filter.
- `ARCHITECTURE.md §6` — canonical human-readable protocol description.

## Tests

- `leak_test.go` `TestNoLeak` — 150 mixed cycles exercise both free paths + the error registry; asserts counters back to baseline (with an `eventually` poll for async frees), goroutines/fds bounded.
- `leak_test.go` `TestAbandonedBatchesFreed` — 10 000 rows (multiple batches), pull one, `Close`, assert the abandoned batches are freed. The deterministic `batch_release` exerciser.
- `TestQueryLogsTypedDecode` — the aliasing regression guard (strings long enough to have failed under the UAF).
- `make leak-valgrind` — the buffer-level gate (env: valgrind 3.22.0). Run with `-tags valgrind` + `sable_safe`.

## Pitfalls

- **Do not release the inner array on the taken path** — the import already nulled its `release`; the shell must free only its box.
- **The abandoned path is easy to under-test** — buffered-but-undrained batches only appear when a query produces > 1 batch (> ~4096 rows); force it with a large ingest.
- **The counter proves the shell, not the buffers** — buffer-level leak-freedom needs the Valgrind gate.
- **Valgrind needs `-tags sable_safe`** or memcheck floods with false stack-pointer errors from sable's asm cgo path; and reading Valgrind's raw "definitely lost" total is wrong — only the libc-malloc-filtered count is the real signal.
- **String aliasing is data-dependent** — a smoke test with tiny values won't catch it; test with long strings.
- A sanitizer-based (ASAN/LSan) third gate is still pending — design new FFI tests so they can also run under one.
