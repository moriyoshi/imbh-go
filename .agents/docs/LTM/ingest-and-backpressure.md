# Ingest Path & Backpressure

## Summary

OTLP ingest (logs / traces / metrics) and flush go over sable's byte-`Call` path — not the streaming Arrow path — because they take protobuf bytes and return a small fixed receipt. Backpressure reuses sable primitives that already existed (`SetMaxInFlight`, `TryCall`, `ErrBackpressure`), giving a single global in-flight cap that also bounds concurrent live result streams. This is the first milestone that exercised IMBH's I-4a lazy segment scan end-to-end (M0 only did constant/`VALUES` SQL).

## Key Facts

- **Ingest is byte-`Call`, not a stream.** Ops: `OP_INGEST_LOGS` (2) / `_TRACES` (3) / `_METRICS` (4) → `db.ingest_otlp_*(&body).await`; `OP_FLUSH` (5) → `db.flush().await`.
- **Request layout:** `[8-byte LE db id][OTLP export-request protobuf]`. IMBH decodes `ExportLogsServiceRequest` etc.
- **Receipt:** a fixed **26-byte** LE record: `accepted(u64) | rejected(u64) | lsn(u64) | durable(u8) | queued(u8)`.
- **Ingest errors surface as a real Go `error`** via sable's byte-`Call` error path (unlike the M0 stream path, which swallowed errors until the M3 error channel).
- **Data is queryable from the buffer immediately after ingest** (no flush needed). `Flush()` seals the buffer into an on-disk segment that the lazy scan then serves — both paths return identical counts.
- **Backpressure:** `SetMaxInFlight(max)` (global cap, 0 = unbounded), `TryIngestOTLP*` via `sable.TryCall` → `ErrBackpressure` at the cap (nothing admitted), plus `RuntimeStats()`/`Stats` (`InFlight`/`Rejected`/`MaxInFlight`). Blocking `IngestOTLP*` stays unbounded.
- **The cap is one global gauge across all admission-controlled entry points.** An open result stream **holds its admission slot until `Close`** (`sable_stream_open` `try_admit`s; `sable_stream_close` `note_complete`s). So the cap bounds concurrent live `Rows` as well as `TryIngest` — this is what makes the backpressure test deterministic.

## Details

### Go API

`ingest.go`: `Receipt` struct + `DB.IngestOTLPLogs/Traces/Metrics([]byte) (Receipt, error)` and `DB.Flush() error`, all over `sable.Call`. Backpressure: `SetMaxInFlight`, `DB.TryIngestOTLP*`, `ErrBackpressure` re-export, `RuntimeStats()`/`Stats` (`type Stats = sable.Stats`).

### `IngestReceipt` upstream drift (fixed)

When IMBH evolved, the receipt shape changed but the Go side did **not** need changing:
- `IngestReceipt.lsn`: `Lsn` → `Option<Lsn>`, and `Lsn` is now `std::num::NonZero<u64>` (was a `Lsn(pub u64)` newtype). `None` while queued for the async-ingest worker. Fix: `r.lsn.map(|l| l.get()).unwrap_or(0)` — absent encodes as 0, which pairs with the `queued` flag the Go `Receipt` already documents.
- `IngestReceipt.queued`: field removed → `is_queued()` method.

### Test data

Built with `go.opentelemetry.io/proto/otlp` + `google.golang.org/protobuf/proto` — the real bytes an OTLP/HTTP exporter sends. This pulled `go.opentelemetry.io/proto/otlp`, `google.golang.org/protobuf` (+ transitive otel sdk) into the Go deps.

## Files

- `rust/src/lib.rs` — `OP_INGEST_*`, `OP_FLUSH` handlers.
- `ingest.go` — `Receipt`, `IngestOTLP*`, `Flush`, `SetMaxInFlight`, `TryIngestOTLP*`, `RuntimeStats`.

## Test Coverage

- `TestIngestAndQuery` (Accepted=3 → buffer `count(*)=3` → `Flush` → segment `count(*)=3`), `TestQueryIngestedBodies` (`WHERE service='api'` → 2), `TestIngestBadBytes` (malformed OTLP → Go error).
- `backpressure_test.go` — saturate a cap of 2 with two live streams, assert a third `Query` **and** a `TryIngest` both return `ErrBackpressure` and `RuntimeStats().Rejected > 0`; `Close` one stream → a new `Query` is admitted (`eventually`). `TestTryIngest` covers the uncapped happy path. **Reset the cap on exit (it is global).**

## Pitfalls

- The in-flight cap is global and shared with result streams — a leaked (unclosed) `Rows` permanently consumes a slot. Always `Close` streams.
- `Flush` is required for cross-reopen durability — it seals the buffer into the on-disk segment a reopened Db reloads (see the durability test).
- Prefer saturating the cap with live streams (deterministic) over racing fast ingests (flaky) when testing backpressure.
