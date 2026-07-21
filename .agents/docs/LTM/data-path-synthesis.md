# The Runtime Data Path & Single-Transport Architecture (synthesis)

## Summary

One architectural decision holds the whole binding together: **streaming Arrow for results, byte-`Call` for scalars and ingest.** Data comes in as OTLP protobuf over byte-`Call`; every read surface — SQL, typed endpoint queries, and the LGTM languages — comes out as Arrow `Rows` over sable's zero-copy streaming cursor. Typed and LGTM "results" are a thin Go-side decode over that single transport, never a second serialization path. Scalars a stream can't carry (query errors, page cursor, `QueryStats`) ride an out-of-band byte-`Call` slot keyed by a query id. This synthesis is the map of that data path, in and out.

## Included Documents

| Document | Focus |
|----------|-------|
| [ingest-and-backpressure.md](./ingest-and-backpressure.md) | Write side: OTLP byte-`Call`, 26-byte receipt, global admission cap |
| [streaming-query-errors-cancellation.md](./streaming-query-errors-cancellation.md) | Read side: per-batch cursor, out-of-band error channel, `NextCtx`, the scalar side-channel |
| [single-transport-typed-queries.md](./single-transport-typed-queries.md) | Arrow-everywhere reframe + Go-side struct decoders |
| [lgtm-query-languages.md](./lgtm-query-languages.md) | PromQL/LogQL/TraceQL over the same Arrow path |

## Stable Knowledge

### The transport split

- **Streaming Arrow (S-3 cursor)** for anything row-shaped and potentially unbounded — SQL, typed queries, LGTM. One batch per sable await; the executor yields at each task boundary. Depends on IMBH's I-4a lazy scan yielding one segment/batch per `poll_next`.
- **Byte-`Call`** for scalars and bounded protobuf: ingest, flush, `count`, admin ops, and the out-of-band side-channel slots.
- **Fully-async, not `spawn_blocking`** — the pull-based one-batch-per-await stream is the core non-blocking mechanism.

### Write side (ingest)

- Ops: `OP_INGEST_LOGS/_TRACES/_METRICS`, `OP_FLUSH`. Request `[8-byte LE db id][OTLP export-request protobuf]`. Receipt = fixed **26-byte** LE record (`accepted|rejected|lsn|durable|queued`).
- **Data is queryable from the buffer immediately after ingest;** `Flush()` seals it into an on-disk segment the lazy scan then serves — identical counts either way. Flush is required for cross-reopen durability.
- Ingest errors surface as a real Go `error` (byte-`Call` error path).
- **Backpressure** — `SetMaxInFlight` (one global admission gauge), `TryIngestOTLP*` → `ErrBackpressure` at the cap, `RuntimeStats`. **An open result stream holds its admission slot until `Close`**, so the cap bounds concurrent live `Rows` as well as `TryIngest` — this is what makes the backpressure test deterministic.

### Read side (query stream)

- Framing `[db id][query id][payload]` via `split_stream_req` (SQL = UTF-8, typed/LGTM = JSON); shared Go `openStream(ctx, op, payload)`.
- **The stream wire has no error channel** — a 0 handle only means "no batch." Terminal errors are stored in `QUERY_ERRORS: Mutex<HashMap<query_id, Vec<u8>>>` *before* the producer drops `tx`, fetched by Go via `OP_QUERY_ERROR` at end-of-stream, returned from `Rows.Err()`. Ordering is safe by construction.
- **Cancellation** via `sable.Stream.NextCtx(ctx)` (a per-pull watcher → `sable_call_cancel`). Never race a `Close` from another goroutine — sable's `Stream` is single-goroutine. `Rows.finish` precedence: import-error → `ctx.Err()` → stored query error.
- **The scalar side-channel pattern generalizes:** any post-drain scalar rides a byte-`Call` slot keyed by query id — `QUERY_ERRORS` → `PAGE_META` is the template (store before streaming, fetch after drain, fetch-even-on-error so the slot never leaks). It carries the `LogPage` cursor and `QueryStats` (both known only after drain, so schema-metadata was infeasible).

### Single transport + Go-side decoders

- **The "typed-struct results" milestone dissolved.** `histogram_quantile` (and `matches`/`json_get_str`/`hex`) are registered DataFusion UDFs reachable through zero-copy SQL today; `metrics().range`/`instant` are SQL; the rest are flat tables or Arrow-rows-plus-a-scalar. So "typed results" = a decode over Arrow, not a second transport.
- **Typed requests are native Go structs → JSON → IMBH builders** (`LogQueryWire`/`MetricQueryWire` → `build_log_query`/`build_metric_query`). Only 3 typed queries are Arrow-shaped (`logs().query_batches`, `metrics().range_batches`, `traces().span_metrics_batches`); they collect **eagerly** because `to_sql`/`sql_with_params` are inaccessible externally (SQL stays the lazy path).
- **Go-side decoders** (`results.go`) use encoding-tolerant readers looked up by column name: `stringAt` (Dictionary/String/LargeString/StringView), `int64At` (Timestamp/Int64/UInt*), `float64At`, `boolAt`, `bytesAt` (FixedSizeBinary), `mapStringAt`/`stringListAt`/`stringFromArray` (LGTM Map/List/StringView). Every string is `strings.Clone`d — see [[ffi-ownership-and-safety-synthesis]].
- **The whole query surface is context-first** — `Foo(ctx, …)`; use `context.Context` for cancellation everywhere.

### LGTM specifics

- Handlers call the Arrow-native `execute_promql_batches`/`execute_logql_batches`/`execute_traceql_batches` and stream the upstream batch directly. Schema: series = `labels: Map<Utf8View,Utf8View>`, `ts: Timestamp(ns)`, `value: Float64`; traces = `{trace_id: Utf8View, span_ids: List<Utf8View>}` one row/trace. Go-facing `Series`/`Point`/`TraceMatch` unchanged.
- **PromQL needs metric resolution** (auto-built from `db.metrics().catalog()`, dots→underscores, 1:1 only — not full Prometheus sanitization). **LogQL has two shapes** — bare selector → log lines (`build_log_query` → `query_batches`), range aggregation → series. **TraceQL attribute scope matters** — `resource.service.name` (resource) ≠ `.service.name` (span) ≠ `name` (intrinsic).

## Operational Guidance

- Row-shaped and potentially unbounded → put it on the streaming Arrow path. Scalar or bounded protobuf → byte-`Call`. A post-drain scalar → the query-id-keyed side-channel slot (fetch even on error).
- Don't build a second serialization transport for "typed results" — decode over Arrow.
- For a long-running byte-`Call` op, use `sable.CallCtx` (it aborts the Rust future), not `sable.Call`.
- When testing backpressure, saturate the cap with live streams (deterministic) rather than racing fast ingests.
- Re-diff LGTM handling against `imbh-tui` when the LGTM surface moves — it is the more complete oracle.

## Files

- `rust/src/lib.rs` — ingest/flush handlers, `OP_SQL`/`OP_QUERY_*`/LGTM handlers, `split_stream_req`, `stream_batches`, `QUERY_ERRORS`/`PAGE_META` + their meta ops, metric-resolution helper.
- `ingest.go` — `IngestOTLP*`, `Flush`, `SetMaxInFlight`, `TryIngestOTLP*`, `RuntimeStats`.
- `db.go` — `Rows`, `openStream`, `Next`/`Close`/`Err`, `finish` precedence.
- `query.go` / `results.go` / `lgtm.go` / `logpage.go` — typed queries, decoders, LGTM, cursor paging.

## Tests

- Ingest/query: `TestIngestAndQuery`, `TestQueryIngestedBodies`, `TestIngestBadBytes`.
- Backpressure (`backpressure_test.go`): saturate cap of 2 with live streams → third `Query` + `TryIngest` both `ErrBackpressure`; `Close` → admitted. **Reset the global cap on exit.**
- Errors/cancel: `TestQueryErrorSurfaced`, `TestQueryCleanEnd`, `TestQueryContextCancel`.
- Typed/LGTM: `TestQueryLogsTypedDecode`, `TestQueryMetricsTypedMatrix`, `TestQuerySpanMetricsTypedRED`, `TestHistogramQuantileViaSQL`, `lgtm_test.go` (six, passed unmodified through the Arrow-native migration — the behavior-preservation proof).

## Pitfalls

- Don't signal stream errors in-band — a 0 handle is genuinely ambiguous; use the out-of-band slot, and fetch it even on the error path or it leaks.
- A leaked (unclosed) `Rows` permanently consumes a global admission slot — always `Close`.
- Typed/LGTM queries collect eagerly upstream — fine for bounded results; SQL is the path for unbounded scans.
- Group-by `g0..gN` label columns are positional — map them back to the query's key names in order.
- Don't reject bare LogQL selectors; TraceQL attribute-scope prefixes change match counts; PromQL name resolution is dots→underscores only.
