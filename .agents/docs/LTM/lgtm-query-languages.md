# LGTM Query Languages (PromQL / LogQL / TraceQL)

## Summary

The binding exposes the LGTM-stack query languages by wiring IMBH's `imbh-lgtm` crate, and — like everything else — returns results over the single zero-copy Arrow path (see [[single-transport-typed-queries]]). PromQL, LogQL range aggregations, and TraceQL all map to Arrow batches decoded into Go structs. The binding is currently *ahead* of the reference `imbh-server`, which exposes only OTLP ingest + SQL. IMBH's own `imbh-tui` consumes the same `imbh_lgtm` pipeline and is a useful oracle for this surface.

## Key Facts

- **Where it lives:** IMBH implements PromQL/LogQL/TraceQL as versioned compatibility *profiles* in `crates/imbh-lgtm` (formerly `imbh-query-language`/`imbh-semantics`, now folded in). A `source` feature adds `*SemanticsExt` execution traits on `MetricsApi`/`LogsApi`/`TracesApi`.
- **Pipeline:** `translate_*(text) → match ImbhQueryModel::X(expr) → db.metrics()/logs()/traces().execute_*_batches(…)` → stream the returned `RecordBatch`.
- **Go API:** `QueryPromQL`/`QueryLogQL`/`QueryTraceQL` (raw Arrow `Rows`) + decoders `…Series → []Series`, `…Matches → []TraceMatch`, and `QueryLogQLLines → []LogEntry` for bare selectors.
- **Results belong on the Arrow path.** LGTM results are tabular (series × time × value) — they stream through the same `Rows` cursor as SQL, not a JSON byte-`Call`.
- Ops: `OP_QUERY_PROMQL`/`_LOGQL`/`_TRACEQL` plus `OP_METRIC_POINTS` (14) and `OP_METRIC_INSTANT` (22) in the same family.

## Details

### Arrow-native execute path (adopted from upstream)

The three LGTM handlers call `execute_promql_batches` / `execute_logql_batches` / `execute_traceql_batches` and stream the upstream batch directly — dropping an earlier manual `Vec<PromSeries>` → Arrow remap. Upstream's batch schema deliberately differs from the old `series_batch`:
- **Series:** `labels: Map<Utf8View,Utf8View>`, `ts: Timestamp(ns)`, `value: Float64`.
- **Traces:** `{trace_id: Utf8View, span_ids: List<Utf8View>}`, one row per trace.

The `Utf8View` payloads are meant to later share the scan's `Arc<Buffer>`s (positions us for future true zero-copy labels). Go `decodeLabeledSeries`/`decodeTraceMatches` read Map/List/StringView/Timestamp via `mapStringAt`/`stringListAt`/`stringFromArray`. **The Go-facing `Series`/`Point`/`TraceMatch` types are byte-for-byte unchanged** — `lgtm_test.go` passed unmodified, the behavior-preservation proof. arrow-go v18.5.1 imports Map/List/StringView/Timestamp over the C Data Interface cleanly.

Note: `metric_instant_handler` deliberately stays on the old `series_batch` schema (`labels:Utf8, timestamp:Int64, value:Float64`) with its own Go decoder — the two paths are isolated.

### Language-specific semantics

- **PromQL needs metric resolution.** `TranslateContext::default()` fails with "metric not resolved." Auto-resolve by enumerating `db.metrics().catalog()` → `MetricResolution{query_name = metric.replace('.', "_"), storage_name = metric, kind}`. Prometheus dots→underscores; `MetricKind`: gauge→Gauge, sum→CumulativeCounter, histogram→CumulativeHistogram. **Resolution is 1:1 + dots→underscores only — not full Prometheus name sanitization** (a known follow-on).
- **LogQL has two result shapes.** `ImbhQueryModel::LogSelector(filter)` (bare selector → log **lines**) and `ImbhQueryModel::Log(expression)` (range aggregation → **series**). `logql_handler` dispatches on the model, mirroring Loki: a selector → `build_log_query` → `logs().query_batches()` → log rows as Arrow (`streams`); a range aggregation → `execute_logql_batches` → series (`matrix`). An early cut wrongly rejected bare selectors — `imbh-tui` showed they are executable.
- **TraceQL attribute scope matters.** `.service.name` (span-scoped) ≠ `resource.service.name` (resource-scoped). Verified: `{ resource.service.name = "checkout" }` → 2, `{ name = "…" }` (intrinsic) → 1, `.service.name` → 0. Correct Tempo semantics. Uses `FetchBounds` (trace-start window), returns matches by trace id + span ids.

### `imbh-lgtm` has no serde

When the binding built the Arrow batch itself (pre-Arrow-native path), it read the result structs directly (`PromSeries{labels: LabelSet, samples: Vec<FloatSample>}`) — there was no serde to lean on. The Arrow-native path removed this concern.

### Naming gotcha

`imbh_lgtm::build_log_query` collides with the binding's own `build_log_query` (the typed-query builder) — imported as `build_log_query as lgtm_log_query`.

## Files

- `rust/src/lib.rs` — `promql_handler`/`logql_handler`/`traceql_handler`, metric-resolution helper.
- `lgtm.go` — `QueryPromQL`/`QueryLogQL`/`QueryTraceQL` + `QueryLogQLLines`, `decodeLabeledSeries`/`decodeTraceMatches`.
- `results.go` — `mapStringAt`/`stringListAt`/`stringFromArray`.

## Test Coverage

- `lgtm_test.go`: `TestPromQLViaArrow`, `TestPromQLParseError`, `TestLogQLViaArrow` (`count_over_time`), `TestTraceQLViaArrow` (`resource.service.name` → 2), plus a bare-selector lines test (`{service="checkout"}` → 2 lines). All six passed unmodified through the Arrow-native migration.

## Pitfalls

- Don't reject bare LogQL selectors — dispatch on `ImbhQueryModel` and run selectors through `build_log_query` for log lines.
- TraceQL attribute scope is a common footgun — `resource.` prefix vs span-scoped vs intrinsic yield different match counts.
- PromQL metric names are resolved 1:1 with dots→underscores only; non-trivial Prometheus sanitization is not yet handled.
- Re-diff against `imbh-tui` when the LGTM surface moves — it exercises this layer more completely than the binding's own tests and has caught genuine gaps.
