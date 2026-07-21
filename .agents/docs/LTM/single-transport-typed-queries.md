# Single-Transport Architecture & Typed Queries

## Summary

Every query surface — SQL, typed endpoint queries, and the LGTM languages — returns Arrow `Rows` over the same zero-copy streaming cursor. There is **one transport (Arrow everywhere) plus a scalar metadata side-channel**, never a second serialization path. "Typed results" are a thin **Go-side decode** over the Arrow path. Typed query *requests* are native Go structs encoded as JSON into IMBH's own builders. The once-planned "typed-struct results" milestone largely dissolved once we proved computed metrics are SQL and everything else is a flat table or Arrow-rows-plus-a-scalar.

## Key Facts

- **Single transport.** SQL, typed queries, and LGTM all return Arrow `Rows`; typed/LGTM results are a Go-side decode over that path, never a second transport.
- **The "typed-struct results" milestone dissolved.** `histogram_quantile` is a registered DataFusion UDF (`imbh-query/src/lib.rs`) reachable through the zero-copy SQL path today — as are `matches`, `json_get_str`, `hex`. `metrics().range`/`instant` are likewise SQL over `metrics_gauge`/`sum`. Other results (`TraceSummary`, `VolumeBucket`, `Trace` spans, `LogPage`) are flat tables or Arrow-rows-plus-a-scalar.
- **Only 3 IMBH typed queries are Arrow-shaped:** `logs().query_batches`, `metrics().range_batches`, `traces().span_metrics_batches`. Each has a native Go request struct + a Go-side struct decoder.
- **Typed queries use native JSON encoding, not proto.** The public Go API is hand-written structs regardless; JSON handles optionals/maps with zero codegen (Go `encoding/json` ↔ Rust `serde_json`).
- **Typed queries are eager** (`*_batches` collect internally) because `to_sql` (private) and `sql_with_params` (`pub(crate)`) are inaccessible externally — SQL stays the only lazy streaming path. See [[imbh-upstream-surface]].

## Details

### The reframe decision

Before committing to a second serialized-struct transport, we checked whether IMBH's typed-struct results are actually non-tabular. They mostly aren't: computed metrics/quantiles are SQL today; the rest are flat tables; genuine non-table residue is tiny (a pagination cursor + the `QueryStats` envelope — scalars that ride the side-channel, see [[streaming-query-errors-cancellation]]). **Decision:** stay single-transport; "typed results" = a Go-side Arrow→struct decoder. Pragmatic exception noted: for tiny point-lookups a struct-return byte-`Call` can be lower-overhead than spinning up a cursor — kept as an option, not the default. Proven: `TestHistogramQuantileViaSQL` returns Prometheus's exact 4.850 as a zero-copy Arrow `Float64`.

### Typed query implementation

- Rust: `serde` + `serde_json`, `LogQueryWire`/`MetricQueryWire`, `build_log_query`/`build_metric_query` mapping onto `imbh::LogQuery`/`MetricQuery` builders. Ops `OP_QUERY_LOGS` (7) / `OP_QUERY_METRICS` (8) / `OP_QUERY_SPAN_METRICS` (9) call `*_batches` and reuse `stream_batches` + the FfiBatch handle path.
- Go: `LogQuery`/`MetricQuery`/`SpanMetricsQuery` structs + `DB.QueryLogs`/`QueryMetrics`/`QuerySpanMetrics`.

### The Go-side decoder layer (`results.go`)

Typed results decode each batch's columns into Go structs via **encoding-tolerant readers** looked up by column name:
- `stringAt` (unwraps `Dictionary(Utf8)`, and `String`/`LargeString`/`StringView`)
- `int64At` (`Timestamp` / `Int64` / `UInt8/32/64`)
- `float64At`, `boolAt`
- `bytesAt` (`FixedSizeBinary` ids)
- `mapStringAt` (`*array.Map`), `stringListAt` (`*array.List`), `stringFromArray` — added for the LGTM Map/List/StringView schema.
- **Every string read is `strings.Clone`d** — see [[arrow-buffer-lifetime-rules]].

Decoders: `QueryLogsTyped → []LogEntry`, `QueryMetricsTyped → Matrix` (rows grouped into `Series` by the `GroupBy` label set; `g_i` columns mapped back to query key names in order), `QuerySpanMetricsTyped → []SpanMetricPoint` (RED), `QueryMetricPointsTyped → []MetricPoint`.

### Column types (logs result)

`time, observed_time, service, severity_number, severity_text, body, attributes, resource, scope, trace_id, span_id, flags`. `service`/`resource`/`scope` are `Dictionary(Utf8)`; result strings are `Utf8` (not `Utf8View`); ids are `FixedSizeBinary`; time is `Timestamp`.

### API shape: context-first

The query surface was later collapsed from a dual `{Foo, FooContext}` form into a single context-taking form — every method is `Foo(ctx, …)`, including the decoder/convenience helpers. Use `context.Context` for cancellation on all query calls.

## Files

- `rust/src/lib.rs` — `LogQueryWire`/`MetricQueryWire`, `build_log_query`/`build_metric_query`, `OP_QUERY_*` handlers, `split_stream_req`.
- `query.go` — `LogQuery`/`MetricQuery`/`SpanMetricsQuery` + `DB.QueryLogs`/`QueryMetrics`/`QuerySpanMetrics`.
- `results.go` — the encoding-tolerant readers + typed decoders.

## Test Coverage

- `TestQueryLogsTypedDecode`, `TestQueryLogsTypedError`, `TestQueryMetricsTypedMatrix`, `TestQuerySpanMetricsTypedRED`, `TestHistogramQuantileViaSQL`, `TestQueryMetricPoints*`.

## Pitfalls

- Do not reach for a second serialization transport for "typed results" — decode over Arrow. The only genuine non-table residue (page cursor, `QueryStats`) belongs on the scalar side-channel.
- Typed queries collect eagerly upstream — fine for bounded results, but SQL is the path for unbounded scans.
- The `g0..gN` group-by label columns must be mapped back to the query's key names *in order* — the column names are positional.
