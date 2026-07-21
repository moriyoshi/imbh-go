# IMBH Upstream Surface: constraints, drift, and the admin/ops mapping

## Summary

The binding consumes IMBH as an external dependency and tracks a moving upstream. This document captures the durable constraints of IMBH's API surface (what forces eager vs lazy, why return structs need hand-mirroring, Arrow column types), the drift that has broken the build and how it was fixed, the admin/ops passthrough surface and op-id map, and the working practices (grep `pub async fn`, use `imbh-tui` as an oracle, expect a mid-edit working tree).

## Key Facts

- **`to_sql` (private) + `sql_with_params` (`pub(crate)`) are inaccessible externally** → typed and LGTM queries route through the **eager** `*_batches` methods; **SQL is the only lazy streaming path**.
- **Only 3 typed queries are Arrow-shaped:** `logs().query_batches`, `metrics().range_batches`, `traces().span_metrics_batches`. IMBH keeps adding more Arrow entry points over time (see drift).
- **The `_batches` signatures are NOT uniform** — check each: `logs().query_batches` returns `Result<Vec<RecordBatch>>`; `metrics().range_batches` and `traces().span_metrics_batches` still return the `(batches, QueryStats)` tuple. The stats-carrying variant of logs is `query_batches_with_stats`.
- **IMBH's return structs derive only `Debug, Clone` — NO serde** (`DbStats`/`TableStats`/`MaintenanceReport`/`CompactionReport`/`SnapshotInfo`/`SegmentRef`, `IngestReceipt`, etc.). Ops-passthrough must mirror each field-by-field into a `*Wire` serde struct, not blanket `serde_json::to_vec`.
- **Grep `pub (async )?fn`, not `pub fn`, when auditing IMBH's API.** Discovery APIs (`attrs().names/values`, `metrics().catalog/series/exemplars`) are `pub async fn` — an early `pub fn` grep missed them and wrongly marked them "not present upstream."
- **Arrow column types:** result strings are `Utf8` (not `Utf8View`); `service`/`resource`/`scope` are `Dictionary(Utf8)`; ids are `FixedSizeBinary`; time is `Timestamp`. LGTM batches use `Map`/`List`/`StringView` (see [[lgtm-query-languages]]).
- **`imbh-tui` is a reliable oracle** for the LGTM/typed surface — it consumes the same `imbh_lgtm` pipeline and has exposed real gaps (e.g. LogQL bare selectors).
- **IMBH's working tree can be mid-edit** — expect transient build breaks (e.g. a half-applied `not_terms` field, a `SegmentBatchIter` mismatch); retry, and re-run the build/test gate against upstream before assuming the binding is current.

## Details

### Breaking-change history (all fixed)

- `IngestReceipt.lsn`: `Lsn` → `Option<Lsn>`, `Lsn` now `NonZero<u64>` (was `Lsn(pub u64)`). Fix `r.lsn.map(|l| l.get()).unwrap_or(0)`; no Go change (0 pairs with the `queued` flag). See [[ingest-and-backpressure]].
- `IngestReceipt.queued`: field → `is_queued()` method.
- `logs().query_batches`: gained the `_with_stats` split (see above).
- Crates bumped `0.0.0 → 0.1.0`; `imbh-query-language`/`imbh-semantics` folded into `imbh-lgtm`; new `imbh-tui` crate + `gen-demo-db` example are workspace members.

### New Arrow entry points adopted

`traces().get_batches(trace_id)` (raw Arrow spans of one trace, `SPAN_COLS` projection — closes the "a `Trace` isn't Arrow-shaped" gap), `metrics().points_batches(query)` (unaggregated counterpart to the resampling `range_batches`), `execute_*_batches` for LGTM. Producer/consumer feature split on the facade: `default = ["ingest","query","search"]`; the binding uses `["cdata","proto","search","serde"]`.

### The admin / lifecycle surface

- **Read-only open:** `OpenReadOnly(path)` → FFI `imbhgo_open_read_only` (`Db::open_read_only`) — no writer lock, coexists with the single writer + other readers; writes rejected.
- **`DbBuilder` options:** `OpenWith(DbOptions)` → FFI `imbhgo_open_opts` (JSON → `DbBuilder` setters in `build_db`): read-only, allow-stale-reads, memory budget, compression (+zstd level), WAL mode (+interval), retention (days/max-disk), refresh (+ttl), background maintenance, promote keys. **Deferred:** the two host-runtime-`Handle` variants (`Maintenance::Runtime`, `Ingest::Async`) need explicit tokio-runtime wiring — a separate design.
- **Ops passthrough:** byte-`Call` ops `Stats`/`Maintain`/`Compact`/`Snapshot`/`Segments`/`SegmentFiles`/`DurableThrough`/`Export` (+`ExportRecords`, Arrow-IPC decode). Request `[db id][JSON args]`; reply JSON of a `*Wire` mirror, or raw Arrow-IPC bytes for export. `Table` has 7 variants (Logs, Spans, 5 metric families), no `FromStr` — `table_from_str` scans `Table::ALL` by `as_str`.

### Op-id map (as consolidated)

| Op | Name | Kind |
|----|------|------|
| 2–4 | `INGEST_LOGS`/`_TRACES`/`_METRICS` | byte-Call |
| 5 | `FLUSH` | byte-Call |
| 6 | `QUERY_ERROR` | byte-Call (side-channel) |
| 1, 7–9 | `SQL`, `QUERY_LOGS`/`_METRICS`/`_SPAN_METRICS` | stream |
| 13 | `GET_TRACE` | stream |
| 14 | `METRIC_POINTS` | stream |
| 15–16 | `ATTR_NAMES`/`_VALUES` | stream |
| 17–19 | `METRIC_CATALOG`/`_SERIES`/`_EXEMPLARS` | stream |
| 20 | `LOG_VOLUME` | stream |
| 21 | `TRACE_SEARCH` | stream |
| 22 | `METRIC_INSTANT` | stream |
| 23–30 | `STATS`..`EXPORT` (admin ops) | byte-Call |
| 31–32 | `LOG_PAGE` / `LOG_PAGE_META` | stream + side-channel |
| 33 | `LOG_COUNT` | byte-Call (scalar) |

(PromQL/LogQL/TraceQL handlers occupy their own ids in the stream family; consult `rust/src/lib.rs` for the exact current numbers — the op-id space grows with each sweep.)

### Discovery / catalog family

`attrs().names/values` → `AttrNames()`/`AttrValues(key)` (`[]string`); `metrics().catalog` → `MetricCatalog() []MetricInfo`; `metrics().series` → `MetricSeries(metric) []string` (label-set stringified via `imbh_core::canonical_json_object` — byte-identical to how imbh parses them back, which required adding `imbh-core` as a direct dep, see [[build-toolchain-and-deps]]); `metrics().exemplars` → `MetricExemplars(metric) []Exemplar` (ids via `to_hex()`, `None` → real SQL-NULL → Go `""`); `logs().volume_by` → `LogVolume`/`LogVolumeBy`. `traces().search` → `SearchTraces(TraceQuery) []TraceSummary` (no upstream `search_batches` needed — the flat `Vec<TraceSummary>` maps to Arrow binding-side).

## Files

- `rust/src/lib.rs` — all handlers + `*Wire` mirrors + `build_db`.
- `admin.go`, `ops.go`, `discovery.go`, `traces_search.go` — the Go-facing admin/discovery surface.

## Test Coverage

`admin_ops_test.go`, `discovery_test.go`, `traces_search.go` tests, `durability_test.go`, `logquery_test.go`. Full suite is 46+ tests under `-race`.

## Pitfalls

- Never assume the binding is current after an upstream change — re-run the full gate; IMBH evolves frequently and its tree can be mid-edit.
- Don't trust a subagent's "clippy clean / tests pass" report — re-verify the gate yourself from source (this has been wrong more than once, e.g. a `collapsible_if` regression reported clean).
- The `Initial.` commit did NOT pass `clippy -D warnings` — the working tree is green now; don't treat the baseline commit as a clean reference.
- Return structs have no serde — mirror them field-by-field; a blanket serialize won't compile.
