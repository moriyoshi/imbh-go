# sable FFI Integration: API shapes, cancellation, and the empty-pointer bug

## Summary

sable is the FFI transport — a Rust-tokio ⇄ Go-scheduler fusion. This document captures the sable API shapes the binding depends on, how cancellation works on both the streaming and byte-`Call` paths (both abort the real Rust-side future, not just the Go wait), and a genuine cross-repo memory-safety bug in sable's empty-result path that concurrency testing surfaced and how it was fixed.

## Key Facts

- **`Payload::Handle` is a tuple variant:** `Payload::Handle(sable::Handle)`; construct via `Handle::new(ptr, release)` (fields private); its `Drop` is the abandoned-path net. Underpins [[zero-copy-arrow-handoff]].
- **`NextCtx` already existed** — cancellation of a parked stream pull needed no sable change, only wiring. `sable.Stream.NextCtx(ctx)` interrupts via a per-pull watcher → `sable_call_cancel(token)`.
- **sable's `Stream` is single-goroutine by contract** — `Next`/`Close` share `s.closed`, so never race a `Close` from another goroutine to cancel; use `NextCtx`.
- **An open stream holds its admission slot until `Close`** → deterministic backpressure (see [[ingest-and-backpressure]]).
- **`sable.CallCtx` genuinely aborts the Rust-side future**, not just the Go caller's wait — so scalar/aggregate byte-`Call` ops are as timeout-safe as the streaming path.
- **The empty-result memory-safety bug:** sable's `sable_call_result` returned `Vec::as_ptr()` for an empty `Vec<u8>`, which is the dangling-but-aligned sentinel `0x1` — landing in a GC-scanned Go pointer slot and crashing under GC stack scan. Fixed in sable at commit `545d04f`.

## Details

### `CallCtx` cancellation is real (traced end-to-end)

- Go `sable.CallCtx(ctx, op, req)` parks for the reply and spawns a watcher goroutine; on `ctx.Done()` it calls `C.sable_call_cancel(token)`. On cancellation the awaited handle is 0 and it returns `ctx.Err()`.
- Rust `sable_call_ctx` (`sable rust/src/lib.rs:1267`) spawns `dispatch(op, req).await` as a tokio task and stores its `abort_handle()` in a `CANCELS` cell. `sable_call_cancel(token)` (`:1310`) looks it up and calls `h.abort()`, which **drops the in-flight future** — the awaited computation (e.g. a DataFusion aggregate) stops being polled.
- The `abort_handle` is registered *after* `spawn`, but the `CancelGuard` is created outside the async block, so its `Drop` (which publishes the cancellation and removes the `CANCELS` entry) runs even if the task is aborted before its first poll — cancel is safe to call the instant the op is issued.

**Reusable rules:**
- For any scalar/aggregate op that can run long, prefer `sable.CallCtx` over `sable.Call` and thread a real `ctx` — cancellation aborts the computation, not merely the caller's wait. (The plain `Call` used by the admin ops in `ops.go` has no such lever — fine for cheap ops, wrong for scan-heavy ones.)
- Inherent limit of a byte-`Call` scalar vs the streamed `Rows` path: no partial result, no early-`Close` — the only control on a long op is cancel/timeout. Acceptable for `count`; when progressive feedback matters use the streamed, bucketed `LogVolume` and sum as batches arrive.
- A read-only count/aggregate is cancel-safe (no partial state) — aborting mid-scan is clean and frees the tokio worker.

`OP_LOG_COUNT` (33) is the canonical example: a byte-`Call` scalar op using `sable.CallCtx` for real cancellation, adding no two-free batch-ownership surface (mirrors `query_error_handler` / `log_page_meta_handler`).

### The empty-result `0x1` GC-stack bug (cross-repo, memory-safety)

Under concurrent load the process intermittently aborted with `runtime: bad pointer in frame ...callResultBytes...: 0x1 / fatal error: invalid pointer found on stack`.

**Root cause (verified from source):** sable's `sable_call_result` did `*out_ptr = r.bytes.as_ptr()`. For an **empty** `Vec<u8>`, `as_ptr()` returns the dangling-but-aligned sentinel `0x1` (align of `u8`). The sable Go shim `callResultBytes` stored that into a `var ptr *C.uint8_t` — a GC-scanned pointer slot. imbh-go's `Rows` teardown calls the byte-`Call` `fetchQueryError` (op 6) on **every** cursor close, and that op returns empty bytes on every clean query — so `0x1` landed in the pointer slot constantly. When a GC stack-scan coincided (amplified by concurrency + low `GOGC`), the runtime rejected it. Sub-page values (< 4096) are exactly what Go's stack scanner flags.

**Fix (both defenses, user-approved):**
1. Rust `sable_call_result`: return `core::ptr::null()` when `bytes.is_empty()` (a clean 0, never a sentinel).
2. Go `callResultBytes`: hold the result pointer as a non-scanned `uintptr`, materialize an `unsafe.Pointer` only transiently and only when `n>0 && ptr!=0`.

Either alone fixes it; both is belt-and-suspenders. Shipped in sable commit `545d04f` (branch `fix/empty-callresult-null-ptr`), which the binding pins (see [[build-toolchain-and-deps]]).

**Verification:** before fix, `TestConcurrentQueries` crashed ~1/25 under `-race`+`GOGC=10` (~15% at default GOGC). After fix: **0 crashes / 40 runs** under `-race`+`GOGC=10`.

**Reusable lessons:** (1) a C-owned pointer must never live in a GC-scanned Go pointer variable; (2) empty Rust slices carry a non-null sub-page sentinel from `as_ptr()`; (3) the robustness/concurrency gate paid for itself immediately — it caught a latent memory-safety defect low-concurrency tests never would.

## Files

- `../sable/rust/src/lib.rs` — `sable_call_result`, `sable_call_ctx`, `sable_call_cancel`, `Payload::Handle`, `Handle`.
- `../sable/call.go` — `callResultBytes` (the `uintptr` fix), `CallCtx`, `Stream.NextCtx`.
- `rust/src/lib.rs` / `db.go` / `ops.go` — the binding's use of these primitives.

## Test Coverage

- `concurrency_test.go` `TestConcurrentQueries` — 48 goroutines × 60 iters, mixed SQL + typed queries on one shared Db; the crash-catcher, run under `GOGC=10 -race`.
- `TestCountLogs` — the `OP_LOG_COUNT` `CallCtx` path.

## Pitfalls

- Never store a C-returned pointer in a GC-scanned Go pointer variable — use a non-scanned `uintptr` and materialize transiently.
- An empty-result byte-`Call` is the common trigger — the `fetchQueryError`-on-every-close pattern made it fire constantly; watch any op that routinely returns empty bytes.
- Use `CallCtx` (not `Call`) for any byte-`Call` op that can run long — it aborts the Rust future; plain `Call` only abandons the Go wait.
