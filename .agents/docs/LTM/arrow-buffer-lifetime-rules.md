# Arrow Buffer Lifetime Rules (the zero-copy footguns)

## Summary

Because result batches cross zero-copy, Go values read out of an Arrow batch can **alias** IMBH-owned memory. Two arrow-go behaviors make this dangerous: the C Data Interface import is a **move** that transfers ownership, and `String.Value(i)` returns a string that **aliases** the value buffer. Any string or `[]byte` kept past `rec.Release()` becomes a use-after-free unless copied first. This bit us with a real, user-visible bug.

## Key Facts

- **arrow-go `cdata` import is a zero-copy MOVE.** `ImportCRecordBatch(*CArrowArray, *CArrowSchema)` nulls the source's `release` on import; the returned `RecordBatch.Release()` alone frees the buffers.
- **arrow-go `String.Value(i)` ALIASES the value buffer** (unsafe, zero-copy) — the returned Go string points into Arrow memory, it is not a copy.
- **Rule: `strings.Clone` every string, and copy every `[]byte`, read out of a batch** if it must outlive the `rec.Release()`.
- This is a **general caller rule**, not decoder-specific: any code reading Arrow strings/bytes and then releasing the batch must copy-out first. The typed-query decoders do this for you.

## Details

### The real bug we hit

The first cut of the Go-side struct decoder (`results.go`) returned garbled strings: short ones ("all ok") survived, long ones ("error happened") came back as garbage. Root cause: the decoder built `LogEntry`s holding aliased strings, then called `rec.Release()`, freeing the IMBH-side buffers. Short strings happened to survive (small-allocation reuse timing); long strings landed in reused memory. Fix: `strings.Clone` every string read out of a batch. Documented on `Rows`, in `ARCHITECTURE.md §6`, and in `README.md`'s zero-copy lifetime-rules section.

### Why the move semantics matter for ownership

The import nulling the source `release` is exactly what makes the [[zero-copy-arrow-handoff]] two-free protocol safe: on the taken path the shell must only free its own box (`shell_free`), never the inner array — the import already took the inner buffers' ownership, and `Record.Release()` is now the sole owner.

## Files

- `results.go` — the decoders (`stringAt`, `bytesAt`, etc.) apply the `strings.Clone` copy-out.
- `db.go` — `Rows`, carries the aliasing caveat in its doc comment.
- `ARCHITECTURE.md §6`, `README.md` — user-facing statement of the rule.

## Test Coverage

- `TestQueryLogsTypedDecode` — exercises Dictionary + String + Timestamp decode with strings long enough to have failed under the aliasing bug (the regression guard).

## Pitfalls

- A caller that reads a raw `*Rows` batch (not the decoders) and keeps strings/bytes past `Release()` re-introduces the UAF — the copy-out obligation transfers to that caller.
- The failure is data-dependent (short strings survive), so it is easy to miss in a smoke test with tiny values — always test with strings long enough to force a fresh allocation.
