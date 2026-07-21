# Zero-Copy Arrow Batch Handoff & the Two-Free Ownership Protocol

## Summary

Query results cross the FFI boundary one Arrow record batch at a time, zero-copy, via the Arrow C Data Interface. Each batch is exported as an `FfiBatch` struct behind a single `u64` pointer carried on sable's `Payload::Handle`. Ownership of that batch is governed by a **two-free protocol**: a batch is freed exactly once, via exactly one of two paths, depending on whether Go took the batch or abandoned it. This is the load-bearing correctness invariant of the whole binding.

## Key Facts

- **Zero-copy = Arrow C Data Interface, not Arrow IPC.** Batches cross as `FFI_ArrowArray` (+ `FFI_ArrowSchema`) by pointer + release callback — no serialize / `GoBytes` copy.
- **A pointer is a `u64`.** sable's completion already delivers a `u64`, so handle payloads needed no new transport — only sable's `Payload::Handle` (S-2) plus the streaming cursor (S-3).
- The exported shell is `FfiBatch { array: FFI_ArrowArray, schema: FFI_ArrowSchema }`, boxed and passed as `Box::into_raw(..) as u64`.
- **Two free paths, one per batch:**
  - **Taken path** — Go imported the batch via `cdata.ImportCRecordBatch`. Go later calls `imbhgo_shell_free`, which frees only the `FfiBatch` box (the inner array/schema `release` were already nulled by the import move — see [[arrow-buffer-lifetime-rules]]). The Arrow buffers are freed later by arrow-go's `Record.Release()`.
  - **Abandoned path** — Go never imported the batch (early `Close`, buffered-but-undrained batches, shutdown with an open cursor). Go calls `imbhgo_batch_release`, which fully drops the `FfiBatch` (its inner `release` callbacks are still live, so this frees the Arrow buffers too).
- Every exported batch is freed exactly once via exactly one path → the `LIVE_BATCHES` counter returns to 0 once cursors quiesce. A positive residual = a leaked shell; a negative = a double free. See [[leak-uaf-verification]].

## Details

### The export path (`export_batch` in `rust/src/lib.rs`)

A `RecordBatch` is converted to an `FfiBatch` shell and `Box::into_raw`'d; `LIVE_BATCHES` is incremented after the `into_raw`. The `u64` pointer rides back on `Payload::Handle`. Rust `into_data()` on the batch's arrays requires `use arrow::array::Array` in scope.

### The sable `Payload::Handle` shape

`Payload::Handle` is a **tuple** variant: `Payload::Handle(sable::Handle)`. Construct via `Handle::new(ptr, release)` (fields are private). The `Handle`'s own `Drop` is the abandoned-path safety net at the sable layer.

### Why two paths, not one

arrow-go's `ImportCRecordBatch(*CArrowArray, *CArrowSchema)` is a zero-copy **move** that **nulls the source's `release`** on import. So after a successful import, the returned `RecordBatch.Release()` is the sole owner of the buffers — the shell must NOT try to release the inner array again (that would double-free). Hence `shell_free` = "forget the inner, free only the box." On the path where Go never imported, the inner `release` is still live, so `batch_release` = "full drop." The protocol is correct either way and was proven over a 2000× `-race` loop in the `proto-cdata/` prototype.

### cgo bridging constraints

- cgo (`import "C"`) cannot live in a `_test.go` file — the bridge lives in a normal `.go`; tests stay pure Go.
- Field addresses of `ffi_batch_array` / `ffi_batch_schema` are taken in C to avoid a `checkptr`-flagged `uintptr → unsafe.Pointer` conversion on the Go side.

## Files

- `rust/src/lib.rs` — `FfiBatch`, `export_batch`, `imbhgo_shell_free` (taken), `imbhgo_batch_release` (abandoned), `LIVE_BATCHES`.
- `db.go` — `Rows` import path (`cdata.ImportCRecordBatch`), calls into the two free shims.
- `imbhgo.h` — the C ABI declarations for the free shims and field-address helpers.
- `ARCHITECTURE.md §6` — canonical human-readable description of the protocol.

## Test Coverage

- `leak_test.go` `TestNoLeak` — 150 mixed cycles exercise both free paths; asserts `imbhgo_live_batches()` back to 0.
- `leak_test.go` `TestAbandonedBatchesFreed` — ingests 10 000 rows (multiple batches), pulls one, `Close`s, asserts the abandoned batches are freed (counter to 0). This is the deterministic `batch_release` exerciser.
- Valgrind buffer gate (`make leak-valgrind`) proves the Arrow buffers themselves — not just the shells — are freed.

## Pitfalls

- Do NOT release the inner array on the taken path — the import already nulled its `release`; doing so double-frees. This is precisely why `shell_free` forgets the inner.
- The abandoned path is easy to under-test — buffered-but-undrained batches only appear when a query produces more than one batch (> ~4096 rows). `TestAbandonedBatchesFreed` forces this with 10 000 rows.
- The in-process counter proves the *shell* balance; buffer-level leak-freedom needs the Valgrind gate (see [[leak-uaf-verification]]).
