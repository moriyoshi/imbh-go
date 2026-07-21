# Streaming Query Path: cursor, errors, cancellation, and the scalar side-channel

## Summary

The SQL query path is fully async and streaming: one Arrow batch per sable await, driving IMBH's lazy per-batch segment scan (I-4a). Because sable's stream wire carries only a `u64` handle (0 = "no batch"), a failed query is otherwise indistinguishable from a clean empty result — so query errors ride an **out-of-band channel keyed by a Go-generated query id**. That same query-id-keyed byte-`Call` side-channel is a reusable pattern: it also carries the `LogPage` cursor and `QueryStats`, which are known only after the stream drains. Cancellation is handled by sable's `NextCtx`, which interrupts a parked pull race-free.

## Key Facts

- **Fully-async streaming, not `spawn_blocking`.** DataFusion is async at batch granularity; a pull-based stream yields the executor at each task boundary (one batch per sable await). Depends on IMBH's I-4a lazy scan yielding one segment/batch per `poll_next` (gated upstream by `scan_reads_one_segment_per_poll`).
- **The stream wire has no error channel** — a 0 handle only means "no batch." So a mid-stream error must be surfaced separately.
- **Out-of-band error channel:** `OP_SQL` request framing is `[8-byte db id][8-byte query id][UTF-8 SQL]`. On any terminal error the Rust handler stores the message in `QUERY_ERRORS: Mutex<HashMap<query_id, Vec<u8>>>` **before** the producer drops `tx`. Go fetches it via `OP_QUERY_ERROR` (op 6) when its stream ends, returns it from `Rows.Err()`.
- **Ordering is safe:** the store precedes the producer dropping `tx`, so by the time Go sees end-of-stream the error is already recorded. Successful queries store nothing (no per-query leak); Go always fetches on terminal, clearing the slot.
- **Cancellation via `sable.Stream.NextCtx(ctx)`** — interrupts a parked `Next` race-free via a per-pull watcher → `sable_call_cancel(token)`. No concurrent `Close` (sable's `Stream` is single-goroutine; `Next`/`Close` share `s.closed`, so a watcher-driven `Close` would data-race).
- **The scalar side-channel pattern generalizes:** any post-drain scalar a stream can't carry rides a byte-`Call` slot keyed by query id. `QUERY_ERRORS` → `PAGE_META` is the template — store before streaming, fetch after drain, fetch-even-on-error so the slot never leaks.

## Details

### Framing evolution

`[db id][query id][payload]` is now shared via `split_stream_req`; SQL payload = UTF-8, typed-query payload = JSON. The Go side shares `db.openStream(ctx, op, payload)` across SQL, typed, and LGTM streams.

### `Rows.finish` precedence

`DB.Query(ctx, sql)` wires `NextCtx`; `Rows.Next` calls `NextCtx`, and `finish()` gives precedence **import-error → `ctx.Err()` → stored query error**, then closes the cursor (aborting the producer, releasing IMBH's snapshot). Consequence: a context cancelled during a query reports `Canceled` even if the producer happened to finish first — accepted semantics.

### The `LogPage` / `QueryStats` side-channel (the keystone that validated the pattern)

- **Decisive upstream finding:** a `LogPage`'s `next` cursor and `QueryStats` are known **only after the query is drained** (`next = offset + rows_returned` iff a full page returns; `stats` built post-collect). So **schema-metadata is infeasible** — the schema is fixed at stream open. This killed the schema-metadata option and forced the byte-`Call`-keyed-by-query-id design.
- `OP_LOG_PAGE` (31) streams the page rows zero-copy via `logs().query_batches_with_stats`; `{next, stats}` ride back through `OP_LOG_PAGE_META` (32), a byte-`Call` mirroring `OP_QUERY_ERROR` (a `PAGE_META` stash next to `QUERY_ERRORS`).
- Go carries the page `Cursor` as **opaque raw JSON it never inspects** — future-proof against the keyset-paging switch the imbh docs flag. `after` round-trips through imbh's serde `PageCursor` (enabled imbh's `serde` feature — pulls no new crate).
- **Trade-off:** `query_batches_with_stats` returns stats but not the LogPage cursor, so the offset-cursor derivation (`prev_offset + rows_returned`, full-page-iff-`rows==limit`) is replicated in the glue — one documented line mirroring `LogsApi::query`. An upstream move to keyset paging invalidates this and must be revisited (see TODO).

## Files

- `rust/src/lib.rs` — `OP_SQL`, `split_stream_req`, `stream_batches`, `QUERY_ERRORS` + `OP_QUERY_ERROR`, `PAGE_META` + `OP_LOG_PAGE`/`OP_LOG_PAGE_META`.
- `db.go` — `Rows`, `openStream`, `Next`/`Close`/`Err`, `finish` precedence.
- `logpage.go` — `QueryLogPage(ctx, LogQuery, Cursor) *LogPage{Entries,Next,Stats}`, `QueryStats`, opaque `Cursor`.

## Test Coverage

- `TestQueryErrorSurfaced` (bad table → real DataFusion planning error via `Rows.Err()`), `TestQueryCleanEnd` (`Err()==nil`), `TestQueryContextCancel` (`errors.Is(Err(), context.Canceled)`).
- `logpage_test.go` — page rows + `{next, stats}` round-trip; leak gates green (meta fetched even on decode error so `PAGE_META` never leaks).

## Pitfalls

- Do not try to signal stream errors in-band — a 0 handle is genuinely ambiguous; the out-of-band slot is the only correct place.
- Do not cancel a stream by racing `Close` from another goroutine — use `NextCtx`; sable's `Stream` is single-goroutine by contract.
- Any new post-drain scalar must fetch its side-channel slot **even on the error path**, or the slot leaks.
