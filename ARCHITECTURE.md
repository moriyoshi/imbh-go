# imbh-go Architecture

This is the canonical architecture document for `github.com/moriyoshi/imbh-go`. It is human-reader-ready and describes the system as it is built. The design rationale, the imbh/sable upstream prescriptions, and the milestone plan behind each decision live in the consolidated [`.agents/docs/PLAN.md`](./.agents/docs/PLAN.md); this document is the durable as-built synthesis. For a shorter agent-facing orientation, see [`.agents/docs/OVERVIEW.md`](./.agents/docs/OVERVIEW.md).

## 1. What imbh-go is

imbh-go is a Go binding of **IMBH** (`../imbh`), a Rust embeddable observability database built on Apache DataFusion, using **sable** (`../sable`), a Rust-tokio ⇄ Go-scheduler fusion runtime, as the FFI transport. A Go program opens an IMBH database, ingests OTLP, runs SQL, typed observability queries, and the LGTM query languages (PromQL / LogQL / TraceQL), and receives results as Apache Arrow record batches transferred **zero-copy** across the language boundary.

Zero-copy has one precise meaning here: query results cross the FFI boundary as **Arrow via the C Data Interface** (`FFI_ArrowArray` + `FFI_ArrowSchema`), imported by Go's `arrow-go/v18` `cdata` package — **not** as Arrow IPC bytes (which would serialize in Rust and `C.GoBytes`-copy in Go), and **not** through sable's byte-buffer `Call` (which would `.to_vec()` in and `C.GoBytes` out). The win is two deleted serialization copies: Go holds IMBH's `Arc`-refcounted Arrow buffers by pointer and drops the Rust-side refcount when done.

## 2. The three components and their boundaries

The defining constraint is that **neither upstream depends on the other**; all glue that knows about both lives in this repo. This is what keeps IMBH and sable independently publishable.

| Component | Repo | Owns | Stays agnostic of |
|-----------|------|------|-------------------|
| **IMBH** | `../imbh` | The query/storage engine. Async, streaming query API (`Db::sql(..).stream()`, `Query::stream`); a lazy per-batch segment scan; the off-by-default `cdata` feature that re-exports Arrow `FFI_*` types; `proto` (typed queries) and `search` (full-text) features. | Go and sable — it gains no dependency on either. |
| **sable** | `../sable` | The FFI transport and runtime fusion: a handler registry (`register` / `register_stream`), a byte `Call(op, req) -> resp` path, a bounded-mpsc streaming cursor (`sable_stream_open/next/close`), and the `Payload::Handle` mechanism that carries a bare `u64` (a pointer) across the boundary with an arm-before-complete release net. | IMBH and Arrow — it gains no dependency on either. |
| **imbh-go** | this repo | The combined Rust staticlib that depends on both as external deps (imbh from crates.io, sable git-pinned; see §3), registers the stream handlers, wraps each `RecordBatch` as an FFI batch, exposes the `imbhgo_*` C ABI; and the Go package that imports sable's Go package and re-hydrates each batch via `arrow-go/cdata`. | — it is the only place that knows all three. |

## 3. Build model — one combined staticlib

```
github.com/moriyoshi/imbh-go            (Go package: DB, Rows, Query)
   │ imports
   ├── github.com/moriyoshi/sable       (built -tags sable_extern_lib → contributes no -lsable)
   │      cgo: OpenStream / Next / Close   (the S-1..S-3 surface)
   └── arrow-go/v18 cdata               (zero-copy import of each FFI_ArrowArray batch)

CGO_LDFLAGS links ONE combined static archive:

rust crate `imbhgo` (crate-type = ["staticlib"])   → rust/target/release/libimbhgo.a
   ├── depends on  sable  (rlib, git-pinned)   → runtime + registry + all sable_* C symbols
   ├── depends on  imbh   (crates.io 0.2.0; features: cdata, proto, search, serde)
   └── imbhgo lib.rs: register_stream(OP_SQL, …) + FFI batch export + free shims
```

**Why one archive.** sable ships the "extern lib" seam (S-5): built with `-tags sable_extern_lib`, its Go package emits no `-lsable`/`-L` of its own (`sable/link_extern.go`), and the embedder points the linker at the combined lib. sable's Rust crate is `["staticlib", "rlib"]` precisely so `imbhgo` can depend on it as an `rlib` and absorb its symbols into `libimbhgo.a`. The result is one archive containing sable's runtime + IMBH + our handlers, with no duplicate `sable_*` symbols (verified by `nm`; no `#[used]` shim needed).

**Toolchain pin.** `go.mod` pins `toolchain go1.26.4` to match sable. The fused runtime reaches Go's internal ABI via `//go:linkname`, and sable certifies its support matrix per `(Go version × arch)`; the binding inherits that matrix. The Go build and tests link `libimbhgo.a`, so it must be built first after any Rust change (`make rust`; `make test` does both).

**Dependency sourcing.** Both upstreams are external deps, not path deps, so this binding is buildable without local checkouts. **imbh** comes from crates.io: `imbh`, `imbh-core`, and `imbh-lgtm` are pinned in lockstep at `0.1.0` (the shared version matters — `imbh-core` must resolve to the same instance as imbh's own transitive `imbh-core`, else `imbh::Attributes != imbh_core::Attributes` in the glue). **sable** is not published; it is a git dep pinned to a **`main`** commit — Rust `sable = { git = "https://github.com/moriyoshi/sable", rev = "0c6fe56" }` (cargo finds the crate in the repo's `rust/` subdir; sable has no tags, so a `main` SHA is the most durable pin available), and Go a **direct** `require github.com/moriyoshi/sable <pseudo-version>` with **no** `replace` (a `replace` applies only to the main module and is ignored for downstream consumers, so the direct require is what makes imbh-go importable). Local checkouts at `../imbh` / `../sable` remain the co-development path; re-pin the deps when upstream changes land there.

## 4. The data path

```
Go goroutine                          sable runtime (Rust)                    imbh::Db (Rust)
  db.Query(ctx, sql) ─ OpenStream ──► sql_stream_handler(op=OP_SQL):
                                        parse [8-byte LE db id][UTF-8 SQL]
                                        db.sql(&sql).stream().await ─────────► SendableRecordBatchStream
  ┌─ loop ─────────────────────────►   stream.next().await → RecordBatch
  │ rec, ok := rows.Next()             export_batch: StructArray::from(batch)
  │                                       → arrow::ffi::to_ffi
  │ ◄─ u64 = *mut FfiBatch ───────────    → Box::into_raw → Payload::Handle(ptr, release)
  │ cdata.ImportCRecordBatch(ptr)         tx.send(payload).await
  │   → arrow.Record (zero-copy)          (send-err ⇒ Go closed cursor ⇒ break, drop stream)
  │ imbhgo_shell_free(ptr)
  └─ rec.Release() ──────────────────► drives the C release → drops IMBH's Arrow refcounts
```

Each `Rows.Next()` is a **discrete sable await that completes**, not a live lazy `FFI_ArrowArrayStream` whose synchronous `get_next` would `block_on` and risk a single-thread deadlock. Iteration is modelled as **repeated completions**.

## 5. Why the query path is fully async (not spawn_blocking)

Because each batch pull is a discrete completing task, sable's single executor thread **yields at a task boundary after every batch** and services other awaits before Go asks for the next one. A query therefore occupies the executor only one batch-quantum at a time, cooperatively interleaved — it never monopolizes the thread for its whole duration. The streaming pull **is** the non-blocking mechanism; neither `spawn_blocking` nor blocking cgo is needed for queries. (Earlier drafts prescribed `spawn_blocking`; that was superseded.)

This property depends on IMBH's **lazy per-batch scan** (prescription I-4a, implemented and verified): `scan()` yields one segment/batch per `poll_next` via `StreamingTableExec` rather than draining all segments into a `MemTable` up front. Without it, `execute_stream()` would block the executor for the whole query at plan time and hold the full result in RAM, failing both the non-blocking and the bounded-memory properties.

Two residual non-yielding quanta remain, both bounded per batch and both owned by **IMBH** (not sable): a **pipeline-breaker** (`sort` / aggregate / `distinct`) front-loads its build into the first `Next()`; and IMBH's segment reads are **synchronous `std::fs`**, so a cold-disk batch blocks the executor for that read. The second is un-fusable — regular files are not epoll-pollable — so it is the only place an optional thread offload might ever be justified.

## 6. The zero-copy batch handoff and the two-free ownership protocol

This is the load-bearing, delicate part of the binding. Each result batch crosses as one `FfiBatch { array: FFI_ArrowArray, schema: FFI_ArrowSchema }` — a `#[repr(C)]` struct, self-describing (schema in every batch, so no separate schema channel), behind a single `u64` pointer carried by sable's `Payload::Handle`.

**Export (Rust, `rust/src/lib.rs`).** `StructArray::from(batch)` (zero-copy) → `arrow::ffi::to_ffi(&sa.into_data())` → box the `FfiBatch`, hand `Box::into_raw as u64` to `Payload::Handle::new(ptr, imbhgo_batch_release)`.

**Import (Go, `db.go`).** `cdata.ImportCRecordBatch(&fb.array, &fb.schema)` — a zero-copy **move** that yields an `arrow.RecordBatch` wrapping the Rust buffers — then `imbhgo_shell_free(ptr)`. The returned batch's `Release()` later drives the C release that frees the Rust Arrow buffers.

There are **two** free functions, and using the right one on each path is the correctness crux:

- **Taken path — `imbhgo_shell_free`** (`std::mem::forget` the inner FFI structs, free only the shell box). Go's import is a move: the returned `arrow.RecordBatch` owns the buffers and its `Release()` drives their release. Rust must not also release them. `forget` guarantees this **regardless of whether arrow-go nulls the source struct on import**, so correctness does not depend on arrow-go's move internals.
- **Abandoned path — `imbhgo_batch_release`** (full `Box` drop → the FFI structs' own `Drop` releases the still-live buffers, then the shell is freed). Used when a batch was buffered in sable's cursor channel and never imported (e.g. the cursor was closed early). This function is the `Payload::Handle` release callback; sable's cursor-drain invokes it on exactly these batches.

Single owner, single free, on both paths. This protocol was validated in isolation before the full binding existed (see §8) and is the highest-priority thing to keep proven under test.

**Value-aliasing caveat (downstream of the same ownership).** A batch's buffers live only until its `Release()`. arrow-go's `String.Value(i)` / binary accessors return values that **alias** the buffer (zero-copy, `unsafe`), so any scalar a caller keeps past `Release()` must be copied out (`strings.Clone`, or copy the `[]byte`). The `Rows` docs warn callers; the Go-side Arrow→struct decoder (`results.go`, e.g. `QueryLogsTyped`) copies every string it materializes. This surfaced as a real use-after-free during decoder development (short strings survived, long ones garbled) and is now covered by tests under `-race`.

## 7. Op protocol and Db handle registry

Op ids are `uint32` constants shared between the Go package and the `imbhgo` crate (a small hand-written table; no codegen initially).

| Op | Kind | Request bytes | Response | Status |
|----|------|---------------|----------|--------|
| `OP_SQL` (1) | stream | `[8-byte LE db id][UTF-8 SQL]` | per-batch `FfiBatch` handles, then end | **implemented (M0)** |
| `OP_QUERY_LOGS` (7) / `OP_QUERY_METRICS` (8) / `OP_QUERY_SPAN_METRICS` (9) | stream | `[db id][query id][JSON query]` | per-batch `FfiBatch` handles | **implemented (M1)** |
| `OP_PROMQL` (10) / `OP_LOGQL` (11) / `OP_TRACEQL` (12) | stream | `[db id][query id][JSON {query,start,end,step}]` | Arrow rows: series → `labels`\|`timestamp`\|`value`; traceql → `trace_id`\|`span_id` | **implemented (LGTM)** |
| `OP_GET_TRACE` (13) | stream | `[db id][query id][JSON {trace_id:"<hex>"}]` | Arrow spans of one trace (`SPAN_COLS` projection) | **implemented** |
| `OP_METRIC_POINTS` (14) | stream | `[db id][query id][JSON {metric,kind,filters,start,end,limit}]` | Arrow raw metric samples (`point_time`, `metric`, `service`, `attributes`, … `value` \| bucket cols) | **implemented** |
| `OP_ATTR_NAMES` (15) / `_VALUES` (16) / `OP_METRIC_CATALOG` (17) / `_SERIES` (18) / `_EXEMPLARS` (19) / `OP_LOG_VOLUME` (20) / `OP_TRACE_SEARCH` (21) / `OP_METRIC_INSTANT` (22) | stream | `[db id][query id][JSON]` | discovery / catalog / search / instant results as Arrow | **implemented** |
| `OP_LOG_PAGE` (31) | stream | `[db id][query id][JSON {query, cursor}]` | a page of log rows as Arrow (`query_batches_with_stats`) | **implemented** |
| `OP_INGEST_LOGS` (2) / `_TRACES` (3) / `_METRICS` (4) | byte `Call` | `[8-byte LE db id][OTLP export-request protobuf]` | 26-byte receipt (`accepted\|rejected\|lsn\|durable\|queued`) or error bytes | **implemented (M2)** |
| `OP_FLUSH` (5) | byte `Call` | `[8-byte LE db id]` | empty on success, else error bytes | **implemented (M2)** |
| `OP_QUERY_ERROR` (6) | byte `Call` | `[8-byte query id]` | terminal error bytes (empty = clean end); removes the entry | **implemented (M3)** |
| `OP_STATS` (23) .. `OP_EXPORT` (30) | byte `Call` | `[db id][JSON args]` | admin ops (`Stats`/`Maintain`/`Compact`/`Snapshot`/`Segments`/`SegmentFiles`/`DurableThrough`/`Export`): JSON `*Wire` reply, or raw Arrow-IPC bytes for export | **implemented** |
| `OP_LOG_PAGE_META` (32) | byte `Call` | `[8-byte query id]` | the page's `{next cursor, QueryStats}` (known only post-drain); mirrors `OP_QUERY_ERROR` | **implemented** |
| `OP_LOG_COUNT` (33) | byte `Call` | `[8-byte db id][JSON log-query]` | 8-byte LE `count(*)` over the filter; uses `sable.CallCtx` (cancellable) | **implemented** |

The `OP_SQL` request carries an extra Go-generated **query id** after the db id — `[8-byte db id][8-byte query id][UTF-8 SQL]` — because the S-3 stream wire has no error channel (a 0 handle only means "no batch"). On a terminal query error the Rust handler stores the message keyed by that id; Go fetches it via `OP_QUERY_ERROR` when the stream ends and returns it from `Rows.Err()`. Context cancellation uses sable's `NextCtx` (a per-pull `sable_call_cancel`), so a parked `Next` is interrupted race-free and `Rows.Err()` reports `context.Canceled`.

This **query-id-keyed byte-`Call` side-channel generalizes**: any scalar a stream can't carry because it is known only after the stream drains rides the same mechanism. The `LogPage` paging cursor and `QueryStats` come back via `OP_LOG_PAGE_META` (the `PAGE_META` stash, mirroring the `QUERY_ERRORS` stash) — the store happens before streaming and Go fetches after draining, even on the error path, so the slot never leaks. Schema-metadata was ruled out precisely because these values do not exist yet when the stream's schema is fixed at open. The `LogPage` cursor is carried opaquely (Go never inspects the JSON token), so an upstream switch to keyset paging would touch only one glue line.

Reads run through `register_stream`: the handler runs the IMBH stream and `tx.send(export_batch(b))`s each batch; a send error means Go dropped the receiver (closed the cursor), which breaks the loop and drops the IMBH stream, releasing its pinned snapshot. Ingest stays on the copying byte `Call` because OTLP bytes are decoded to Arrow immediately, so zero-copy-in buys nothing.

The `imbhgo` crate owns a `HashMap<u64, Arc<imbh::Db>>` keyed by an opaque id; `imbhgo_open` / `imbhgo_open_memory` insert and return an id, `imbhgo_close` removes it. Every request is prefixed with the 8-byte db id so the stateless stream handler can recover its `Db`. Opening is a synchronous cgo call, not a sable await.

**Init ordering.** `imbhgo_init()` must `register_stream` **before** sable builds its runtime. It is idempotent (guarded by a `Once`) and called from the Go package's `init()` immediately before `sable.Init()`. Registration is explicit rather than `#[ctor]`-based, for deterministic ordering with no extra dependency.

## 8. C ABI surface

Declared in `imbhgo.h`, defined in `rust/src/lib.rs` (`#[unsafe(no_mangle)]`), plus sable's own `sable_*` symbols absorbed into the same archive:

```
void     imbhgo_init(void);                              // register handlers (idempotent)
uint64_t imbhgo_open_memory(void);                       // open in-memory Db → id (0 = error)
uint64_t imbhgo_open(const uint8_t *path, size_t len);   // open on-disk Db → id (0 = error)
uint64_t imbhgo_open_read_only(const uint8_t *path, size_t len);  // reader Db (no writer lock) → id
uint64_t imbhgo_open_opts(const uint8_t *json, size_t len);       // DbBuilder options (JSON) → id
void     imbhgo_close(uint64_t id);                      // drop a Db handle
void     imbhgo_shell_free(uint64_t ptr);                // taken-path free (see §6)
int64_t  imbhgo_live_batches(void);                      // leak gate: live FfiBatch shells (0 quiesced)
uint64_t imbhgo_live_dbs(void);                          // leak gate: open Db handles
uint64_t imbhgo_pending_query_errors(void);              // leak gate: un-fetched query errors
```

The abandoned-path free (`imbhgo_batch_release`) is not exported by name; it is carried as the `Payload::Handle` release callback and invoked by sable. The three leak-gate accessors let the test suite assert that every exported batch is freed exactly once (counter returns to 0; negative = double free) and that the registries drain.

## 9. Status and milestones

Both upstreams are complete and verified: IMBH (`cdata`, `collect_with_schema`, `run_sql_stream` / `Query::stream`, the I-4a lazy per-batch scan with its `scan_reads_one_segment_per_poll` gate, and — added and adopted later — the Arrow-native `imbh-lgtm` `execute_*_batches` path) and sable (S-1 handler registry, S-2 handle payload, S-3 streaming Call, S-4.2 multithread IO-disabled executor, S-5 extern-lib seam, plus a later empty-result FFI memory-safety fix pinned at `545d04f`).

- **M0 — walking skeleton (open → SQL → zero-copy rows). Done.** The whole path works end-to-end under `-race`: the combined `libimbhgo.a` builds, all `sable_*` + `imbhgo_*` symbols are retained, and tests for a constant `SELECT`, a multi-row `VALUES` query, and the early-close cancel path pass; `gofmt` / `go vet` clean. M0 exercises the transport and the DataFusion stream but not the IMBH segment scan (that needs ingest, M2). The arrow-go `cdata` move semantics — the one real unknown — were retired first by an isolated prototype in `proto-cdata/` (validated against arrow-go v18.5.1: `ImportCRecordBatch` is a zero-copy move that nulls the source's `release`; the two-free protocol holds under a 2000× `-race` loop).
- **M1 — typed queries. Done (Arrow-shaped surface).** Native Go query structs (`LogQuery`, `MetricQuery`, `SpanMetricsQuery`) marshalled as **JSON** and mapped, Rust-side, onto IMBH's own typed builders — not proto (the public API is native structs regardless; JSON avoids a codegen step). They run via IMBH's public **eager** `*_batches` APIs (`to_sql`/`sql_with_params` are private, so the lazy path is unavailable to an external crate) and stream out through the same `Rows`/`FfiBatch` path. All three Arrow-shaped typed queries are implemented (`DB.QueryLogs`, `QueryMetrics`, `QuerySpanMetrics`, each taking a `context.Context` first argument), plus **Go-side decoders** returning typed structs: `QueryLogsTyped → []LogEntry`, `QueryMetricsTyped → Matrix` (rows grouped into series by `GroupBy`), `QuerySpanMetricsTyped → []SpanMetricPoint` (RED: calls/errors/p50/p95/p99). Verified under `-race`: log service/full-text/limit filters, gauge-range Matrix, and span RED metrics (3 spans, 1 errored → calls=3/errors=1). Note: these collect eagerly (bounded results); use SQL for unbounded scans. **Deferred (separate milestone):** the typed-*struct* results that aren't row-shaped — a single assembled `Trace`, `LogPage` pagination cursor — plus the reframe finding that computed metrics (`histogram_quantile`, range, instant) are already reachable via SQL today (see §9 note).
- **M2 — ingest. Done.** `OP_INGEST_*` + `OP_FLUSH` byte ops; Go `DB.IngestOTLPLogs/Traces/Metrics` + `DB.Flush`. Verified under `-race`: ingest real OTLP → query buffer → flush → query the sealed segment (driving IMBH's I-4a lazy scan), plus a `WHERE service='api'` predicate and the byte-Call error path. First milestone hitting a real IMBH table.
- **M3 — errors & cancellation. Done.** Stream query errors surface via the out-of-band `OP_QUERY_ERROR` channel (query-id keyed) and are returned from `Rows.Err()`; `DB.Query(ctx, sql)` cancels a parked `Next` via sable's `NextCtx` and reports `context.Canceled`. Verified under `-race`: a bad-table query surfaces the real DataFusion planning error, a clean query reports `Err()==nil`, and a cancelled context terminates iteration with `context.Canceled`. Remaining M3 scope (ingest backpressure) is deferred.
- **M4 — hardening. Done.** Two leak gates are in place:
  - **In-process counter (`leak_test.go`, `-race`):** 150 mixed ingest/query/close cycles plus a deterministic multi-batch early-close leave the live-batch counter at 0 (two-free protocol balanced — no leak, no double free), drain the query-error and Db registries, and return goroutines/fds to baseline.
  - **Valgrind buffer gate (`make leak-valgrind`):** runs the binding under Valgrind with Go's `-tags valgrind` runtime instrumentation (CL 674077) and `-tags sable_safe` (plain cgo, so Valgrind isn't tripped by sable's asm/g0 fast path), asserting **zero** definite-loss blocks allocated via libc `malloc`. This proves the Arrow *buffers* and shells are freed on both ownership paths — Rust's allocator uses libc malloc (Valgrind-traceable), whereas Go's GC heap is not, so the ~370 KB Valgrind reports "definitely lost" is entirely Go false positives (`runtime.mallocgc` / stdlib+deps init / arrow-go's Go-side import wrappers), and the libc-malloc count is 0.

  **Ingest backpressure done:** `SetMaxInFlight(n)` caps concurrently in-flight admitted work (global — bounds `TryIngestOTLP*` and open result streams, since a live `Rows` holds an admission slot until `Close`); `TryIngestOTLP*` return `ErrBackpressure` at the cap; `RuntimeStats()` exposes `InFlight`/`Rejected`/`MaxInFlight`. Verified deterministically under `-race` (two live streams saturate a cap of 2 → a third `Query` and a `TryIngest` are refused; freeing a slot re-admits).

  **Robustness gates and surface completion (also done).** A durability-reopen gate (`Open(TempDir)` → ingest → `Flush()` → `Close` → reopen → data survives) and a concurrency-under-load gate (48 goroutines × 60 iters of mixed SQL + typed queries on one shared `Db`) — the latter surfaced and fixed a real **sable FFI memory-safety bug** (an empty-result `Vec::as_ptr()` returned the sub-page sentinel `0x1`, which crashed under Go's GC stack scan; fixed both sides and captured by the `545d04f` pin). The typed surface was filled out to near-parity with IMBH's facade: `LogPage` cursor paging + `QueryStats`, `metrics().instant`, raw metric points, `traces().search`, discovery/catalog (`AttrNames`/`AttrValues`/`MetricCatalog`/`MetricSeries`/`MetricExemplars`/`LogVolume`), `logs().count`, read-only opens, `DbBuilder` options, and an admin/ops passthrough (`Stats`/`Maintain`/`Compact`/`Snapshot`/`Segments`/`Export`). The query API is context-first throughout (`Foo(ctx, …)`).

  Still pending: an Arrow-native `Rows` convenience iterator; a sanitizer (ASAN/LSan) leak gate; and infra (CI, an amd64 run).

- **Externalization. Done.** The path deps were replaced (see §3): imbh from crates.io `0.1.0` (lockstep `imbh`/`imbh-core`/`imbh-lgtm`, `serde` feature added for the paging cursor), sable git-pinned at `0c6fe56`, a **`main`** commit that carries both the memory-safety fix (PR #1) and the Apple-target port (PR #2). **The "pin is on a fix branch, not `main`" follow-up is closed** — both upstream branches are merged and the pin now tracks `main`.

- **LGTM query languages (PromQL / LogQL / TraceQL). Done.** Wired IMBH's `imbh-lgtm` crate (built with its `source` feature): the query text is parsed (`translate_*`) and executed against `imbh::Db` (`*SemanticsExt`), and — the key decision — the result is **streamed on the same zero-copy `Rows` path as everything else**, not returned as JSON structs. The handlers call IMBH's Arrow-native `execute_promql_batches`/`execute_logql_batches`/`execute_traceql_batches` and stream the upstream batch directly (an earlier manual `Vec<PromSeries>` → Arrow remap was dropped once upstream shipped this path). Schema: series = `labels: Map<Utf8View,Utf8View>` \| `ts: Timestamp(ns)` \| `value: Float64`, decoded Go-side into `[]Series`; TraceQL = `{trace_id: Utf8View, span_ids: List<Utf8View>}` one row per trace → `[]TraceMatch`. `DB.QueryPromQL`/`QueryLogQL`/`QueryTraceQL` (raw Arrow) + `…Series`/`…Matches` (decoded), each context-first. PromQL metric names are auto-resolved against the Db catalog (dots→underscores, Prometheus convention). LogQL dispatches both bare selectors (→ log lines) and range aggregations (→ series); out-of-profile constructs are rejected with a stable diagnostic. Verified under `-race`; the six LGTM tests passed unmodified through the Arrow-native migration (behavior-preservation), and `imbh-tui` is the upstream oracle to re-diff against when the LGTM surface moves.

## 10. Source map

| Path | Role |
|------|------|
| `rust/src/lib.rs` | The `imbhgo` staticlib: Db registry, `imbhgo_*` C ABI, `export_batch`, the two free shims, the `OP_SQL` stream handler, the query-error registry (`OP_QUERY_ERROR`), and the `OP_INGEST_*`/`OP_FLUSH` byte handlers. |
| `rust/Cargo.toml` | Combined staticlib crate; depends on sable (git-pinned rlib) + IMBH crates.io (`cdata`, `proto`, `search`, `serde`) + `imbh-lgtm` (`source`) + a direct `imbh-core` (lockstep, for `canonical_json_object`/`to_hex`). |
| `imbhgo.h` | Hand-written C ABI header; keep in sync with the `#[unsafe(no_mangle)]` fns. |
| `db.go` | Go package: `DB.Open` / `OpenInMemory` / `Close`, `DB.Query(sql) → Rows`, `Rows.Next` / `Close`; the cgo bridge (Arrow C Data Interface struct definitions) and `init()`. |
| `ingest.go` | Go package: `Receipt`, `DB.IngestOTLPLogs/Traces/Metrics`, `DB.Flush`, and backpressure (`SetMaxInFlight`, `TryIngest*`, `RuntimeStats`). |
| `query.go` | Go package: typed queries `LogQuery`/`MetricQuery`/`SpanMetricsQuery` + `DB.QueryLogs`/`QueryMetrics`/`QuerySpanMetrics` (JSON → IMBH builders); plus raw samples `MetricPointsQuery` + `DB.QueryMetricPoints`/`…Typed`. |
| `results.go` | Go-side Arrow→struct decoders: `QueryLogsTyped → []LogEntry`, `QueryMetricsTyped → Matrix`, `QuerySpanMetricsTyped → []SpanMetricPoint`, plus encoding-tolerant `stringAt`/`int64At`/`float64At`/`bytesAt` readers (Dictionary/Timestamp/FixedSizeBinary; strings copied out). |
| `lgtm.go` | Go package: LGTM query languages `DB.QueryPromQL`/`QueryLogQL`/`QueryTraceQL` (raw Arrow) + `…Series`/`…Matches` decoders (`[]Series` / `[]TraceMatch`); plus `DB.GetTrace`/`GetTraceSpans` (one trace's spans as Arrow → `[]Span`), which composes with TraceQL matches. |
| `logpage.go` | Go package: `DB.QueryLogPage → *LogPage{Entries, Next, Stats}` (zero-copy rows + the `OP_LOG_PAGE_META` scalar side-channel); opaque `Cursor`, `QueryStats`. |
| `discovery.go` | Go package: `AttrNames`/`AttrValues`, `MetricCatalog`/`MetricSeries`/`MetricExemplars`, `LogVolume`/`LogVolumeBy` (flat `Vec<T>` → one Arrow batch → typed decode). |
| `traces_search.go` | Go package: `DB.SearchTraces(TraceQuery) → []TraceSummary`, plus `metrics().instant` / raw metric points helpers. |
| `admin.go` | Go package: `OpenReadOnly`, `OpenWith(DbOptions)` (the `imbhgo_open_read_only`/`imbhgo_open_opts` FFI). |
| `ops.go` | Go package: the admin ops passthrough (`Stats`/`Maintain`/`Compact`/`Snapshot`/`Segments`/`SegmentFiles`/`DurableThrough`/`Export`+`ExportRecords`) over byte `Call` ops 23–30. |
| `debug.go` | Leak-gate accessors (`liveBatches`/`liveDBs`/`pendingQueryErrors`) into the Rust counters. |
| `proto-cdata/` | The isolated arrow-go `cdata` ownership prototype that retired risk #1. |
| `Makefile` | `make rust` builds the archive; `make test` builds it then runs `go test -tags sable_extern_lib -race ./...`. |
| `.agents/docs/PLAN.md` | The consolidated design rationale, imbh/sable upstream prescriptions, binding milestones, and the I-4a lazy-scan detail. |
