# imbh-go Plan

The consolidated planning and prescription record for the zero-copy IMBH Go binding. This is the "how we designed it, what each upstream had to change, and what remains" document; the as-built system is described in the canonical root [`ARCHITECTURE.md`](../../ARCHITECTURE.md). This file merges the design plans and upstream prescriptions that formerly lived as separate files under `docs/` (`00-zero-copy-integration-plan.md`, `prescription-imbh.md`, `prescription-sable.md`, `impl/binding-implementation-plan.md`, `impl/imbh-lazy-scan.md`).

## Status at a glance

- **Both upstreams are complete and verified.** IMBH landed I-1..I-4a (arrow `cdata`, owned-batch invariant, `collect_with_schema`, `run_sql_stream` / `Query::stream`, and the lazy per-batch scan), plus the later Arrow-native `imbh-lgtm` `execute_*_batches` path. sable landed S-1..S-5 (handler registry, handle payload, streaming Call, multithread IO-disabled executor, library structure), plus a later empty-result FFI memory-safety fix (pinned at `545d04f`; see Part III).
- **Binding: M0..M4 are all done, and the surface is filled out well beyond the original milestones.** SQL, OTLP ingest + flush (M2), the stream-path error channel + context cancellation (M3), backpressure and the two leak gates (M4), the three Arrow-shaped typed queries + Go-side decoders (M1, via **JSON** not proto — see §4.4), the LGTM languages, `LogPage` paging + `QueryStats`, discovery/catalog/search/instant, read-only + `DbBuilder` opens, an admin/ops passthrough, and durability + concurrency-under-load gates. All under `-race`.
- **Remaining work is infra only**: CI and an amd64 run. (The long-standing "re-pin sable off a fix branch" item is **closed** — the pin is now the `main` commit `30b2c30`.) Deferred features (not correctness): an Arrow-native `Rows` iterator, a sanitizer leak gate, the two host-runtime `DbBuilder` variants. See §4.4 and `.agents/docs/TODO.md`.

---

# Part I — The zero-copy design

Zero-copy has one precise meaning: query results cross the FFI boundary as **Arrow via the C Data Interface** (`FFI_ArrowArray` / `FFI_ArrowSchema`), imported by Go's `arrow-go/cdata` — **not** as Arrow IPC bytes (serialize in Rust, `C.GoBytes`-copy in Go), and **not** through sable's byte-buffer `Call` (`.to_vec()` in, `C.GoBytes` out). The Arrow buffers DataFusion produces are `Arc`-refcounted heap allocations; the C Data Interface hands Go a pointer plus a release callback, so Go bumps nothing and copies nothing and drops the Rust-side refcount when done. The win is **two deleted serialization copies** (IPC encode + `GoBytes`).

## The key finding — why the MVP is cheap

sable's completion already delivers a bare `u64` result (`Inner::complete(token, result: u64)`; `AwaitToken(spawn) uint64` on the Go side). **A pointer is a `u64`.** So a one-shot handle crosses on the existing completion path with **no new sable transport primitive**. The only genuinely new capability the MVP needs is a pluggable async handler registry, so an IMBH query op can be hosted at all.

## Phasing

| Phase | What crosses | Memory | Executor occupancy | New sable primitive | New imbh work |
|---|---|---|---|---|---|
| **1 — MVP zero-copy** | one handle over an in-memory `Vec<RecordBatch>` | fully materialized in RAM (same as `collect`) | whole query occupies the executor during `collect` | none beyond the registry | enable arrow `ffi`; hand collected batches out |
| **2 — streaming zero-copy** | successive `FFI_ArrowArray` batches pulled lazily | bounded — never materializes the whole result | one batch at a time, yields between batches | streaming Call (async `get_next`) | `run_sql_stream` → `Query::stream`, lazy per-batch scan |

Phase 1 deletes both serialization copies (the actual zero-copy goal) while still collecting into RAM. Phase 2 additionally **bounds memory and keeps the runtime non-blocking**. The delivered binding targets the Phase-2 streaming model directly (both upstreams shipped the streaming pieces).

## Streaming is the "fully async" mechanism, not just a memory bound

`FFI_ArrowArrayStream.get_next` is a **synchronous** C callback; pulling the next batch from a DataFusion `SendableRecordBatchStream` is **async**. The design resolves this by modelling iteration as **repeated sable awaits** — each `Next()` is a fresh completion — rather than a live lazy `FFI_ArrowArrayStream` whose `get_next` would `block_on` (blocking a Go M, risking a single-thread deadlock).

The consequence: because each batch pull is a **discrete task that completes**, sable's single executor thread **yields at a task boundary after every batch** and services other awaits before Go asks for the next one. A query occupies the executor only one batch-quantum at a time, cooperatively interleaved — it never monopolizes the thread. This is why the design needs neither `spawn_blocking` nor blocking cgo for queries. (Earlier drafts prescribed `spawn_blocking`; that was over-cautious and is superseded.)

**Two residual non-yielding quanta remain**, both bounded per batch and both owned by **IMBH** (not sable). They are referenced throughout this document as "the two residual quanta":

1. **Pipeline-breakers** — `sort` / hash-aggregate / `distinct` drain all input before the first output batch, so the first `poll_next` runs the whole build in one poll. The lever is pruning to keep the build set small (Tantivy `RowSelection` + bloom), not offloading.
2. **Cold-disk reads** — IMBH's segment reads are synchronous `std::fs` (`ParquetRecordBatchReaderBuilder<File>`), so a cold-page batch blocks the executor for that read (warm/page-cache: microseconds). This is un-fusable — regular files are not epoll-pollable — so it is the only place an optional thread offload might ever survive.

## Division of labor (the invariant that keeps all three publishable)

- **IMBH** owns enabling the version-matched Arrow C Data Interface and exposing results as a `SendableRecordBatchStream`. It stays **Go-agnostic and sable-agnostic**.
- **sable** owns the handler registry and the streaming Call (the non-blocking mechanism). It stays **imbh-agnostic and arrow-agnostic**.
- **imbh-go** (this repo) owns the combined Rust staticlib that depends on both, registers the handlers, does the batch → FFI export, and the Go package with the `cdata` import glue. Neither upstream takes a dependency on the other; the glue lives here.

## Non-goals

- **Ingest zero-copy.** OTLP bytes are decoded (protobuf → Arrow) immediately, so the input copy is dwarfed by decode; the query descriptor on the read path is tiny. Ingest stays on the existing copying `Call`.
- **Typed result structs** (`LogPage`, `Trace`, `Matrix`). The zero-copy surface is Arrow batches; typed shapes, if wanted, are decoded from the Arrow on the Go side.

---

# Part II — IMBH prescription (`../imbh`)

IMBH stays Go-agnostic and sable-agnostic throughout — it gains no dependency on Go, sable, or the binding. All items are **complete and verified**.

| ID | Kind | Status | One-liner |
|----|------|--------|-----------|
| I-1 | Cargo + re-export | ✅ done | opt-in `cdata` feature → `arrow/ffi`; re-export `FFI_Arrow*` |
| I-2 | invariant + test | ✅ done | guarantee result batches are owned and segment-independent |
| I-3 | ergonomics | ✅ done | `collect_with_schema` helper |
| I-4 | new API | ✅ done | `run_sql_stream` → `Query::stream` → `SendableRecordBatchStream` |
| **I-4a** | execution fix | ✅ done & verified | `StreamingTableExec` + lazy `SegmentBatchIter` — one segment/batch per `poll_next` |
| I-5 | optional | ⏳ deferred | carry `QueryStats` on the stream (accessor or schema metadata) |

**I-1 — Arrow C Data Interface behind a feature.** `arrow::ffi` is gated by arrow's `ffi` feature, which DataFusion's trimmed feature set did not pull. The binding **must** use IMBH's re-exported arrow (a second, separately-versioned `arrow` crate would produce ABI-incompatible `FFI_ArrowArray` structs). IMBH added an off-by-default `cdata` feature (`arrow 58.3.0 features=["ffi"]`) and re-exports `FFI_ArrowArray` / `FFI_ArrowSchema` / `FFI_ArrowArrayStream`. Off-by-default keeps the shipping graph unchanged for non-binding embedders. This is the "single arrow version" rule — hold it.

**I-2 — result batches are FFI-safe.** Zero-copy means Go holds Rust-owned Arrow buffers after the call returns, past IMBH's segment reclaim (retention / `compact()` unlink segments). Parquet is decoded into freshly-allocated `Arc`-refcounted Arrow — not a borrow of file bytes — so collected batches are self-contained and outlive their source segment. IMBH made the guarantee explicit (doc invariant + a test that exports a result, `compact()`s + retention-unlinks every segment the query read, then drains and asserts the data is intact). The one hazard to watch is a batch sliced zero-copy from the live mutable buffer; if real, force a copy on the export boundary for buffer-sourced batches.

**I-4 — expose `SendableRecordBatchStream`.** Tapped at `df.execute_stream().await` (instead of `df.collect().await`) in `imbh_query::run_sql`. **Lifetime hazard handled**: `run_sql` builds a fresh `SessionContext` per call whose providers hold `Arc`s into the snapshot tables; the returned stream is `'static` only if it owns everything it touches, so it captures the context and provider `Arc`s (self-rooting; a test drops every local binding except the stream and still drains it). Read-only snapshot semantics carry over: the snapshot is fixed at `execute_stream()` time and segment paths are pinned for the stream's life.

**I-4a — the scan must yield one batch per `poll_next` (the load-bearing item).** Exposing the stream at the API is necessary but not sufficient. The original defect: `SegmentTableProvider::scan` read **every** segment synchronously into a `Vec` wrapped in a `MemTable`, so `execute_stream().await` blocked the executor for the whole query's read+decode at plan time and held the full result in RAM — a streaming API over a non-streaming scan; both the non-blocking and bounded-memory properties failed, and the `run_sql_stream` "bounded-memory" doc comment was inaccurate. The fix (detail in Part V) makes `scan()` return a `StreamingTableExec` over a lazy per-batch stream. Verified: the gate test `scan_reads_one_segment_per_poll` passes (0 segments read at plan time, 1 after the first poll, N after drain), plus the facade equivalence/stats tests and all query-facade tests; `clippy -p imbh-query` clean.

**I-5 — `QueryStats` on the stream (imbh-side option still deferred; the binding delivers stats another way).** Stats (segments read vs. pruned, rows materialized, whether Tantivy was consulted) are only fully known after the scan completes. The two upstream options — a `stats()` accessor on the stream wrapper, or `QueryStats` on the stream's schema metadata — remain unbuilt in imbh. The **binding** instead delivers `QueryStats` (and the `LogPage` cursor) over its query-id-keyed byte-`Call` side-channel (`OP_LOG_PAGE_META`), fetched after the stream drains; this is exactly why schema-metadata was ruled out binding-side — the values do not exist when the stream's schema is fixed at open. So the Go surface has `QueryStats` today without the imbh-side I-5 change.

---

# Part III — sable prescription (`../sable`)

sable stays imbh-agnostic and arrow-agnostic — its job is to host someone else's async handlers and deliver opaque handles (not just bytes) across the completion path. All items are **complete and verified**; every change is additive (none touch the doorbell, park state machine, dispatcher, or the single-epoll invariant).

| ID | Kind | Status | One-liner |
|----|------|--------|-----------|
| S-1 | new Rust API | ✅ done | pluggable async handler registry (`register` / `register_stream`); demo becomes one registrant |
| S-2 | new C ABI (2 fns) | ✅ done | `Payload::Handle` + `sable_call_handle` / `sable_call_handle_taken`, single-owner free with a release-on-drop net |
| S-3 | new C ABI (3 fns) | ✅ done | streaming cursor (`sable_stream_open` / `_next` / `_close`) — async `get_next` via repeated awaits |
| S-4 | guidance | ✅ (spawn_blocking correctly absent) | prefer S-3 streaming over any offload; `spawn_blocking` demoted to an optional cold-disk scalpel |
| S-5 | build/structure | ✅ done | sable's runtime consumable as a library in the binding's combined staticlib |

**S-1 — pluggable async handler registry.** Previously `sable_call` hardwired `demo::dispatch(op, req)`, which the binding could not extend. sable is now a library crate an embedder builds their own staticlib against, registering handlers before `Init`. The payload type generalizes:

```rust
pub type CallResult = Result<Payload, Vec<u8>>;   // Ok(payload) | Err(error bytes)

pub enum Payload {
    Bytes(Vec<u8>),
    /// (raw pointer as u64, release fn). sable delivers `ptr` as the completion result and, if Go
    /// never takes it, calls `release(ptr)` exactly once. sable never inspects the handle.
    Handle { ptr: u64, release: unsafe extern "C" fn(u64) },
}

pub fn register(op: u32, handler: impl AsyncHandler);         // one-shot byte/handle ops
pub fn register_stream(op: u32, handler: impl StreamHandler); // streaming ops (S-3)
```

`dispatch` checks the registry first and falls back to `demo` (now behind a default-on `demo` feature the embedder can drop). Implementation note learned during M0: `Payload::Handle` is a **tuple** variant `Payload::Handle(sable::Handle)`; `Handle::new(ptr, release)` is the constructor (fields private); `Handle` has its own `Drop` = the abandoned-path release net; sable re-exports `Handle` at the crate root.

**S-2 — deliver an opaque handle, free it correctly.** New C ABI (byte entry points unchanged):

```c
void sable_call_handle(const SableRuntime *rt, uint32_t op, const uint8_t *req, size_t req_len, uint64_t token);
void sable_call_handle_taken(uint64_t token);  // disarms sable's release-on-drop net for that handle
```

Ownership contract: the handler returns `Payload::Handle`; sable records `(token → release)` and delivers `ptr` as the `u64` completion. Normal path — Go receives `ptr`, calls `sable_call_handle_taken(token)` (drops the entry **without** calling `release`; Go now owns it), imports it, and the import drives the release on `Release()`. Abort/shutdown/never-taken path — sable's teardown (arm-before-complete, mirroring the `CancelGuard` discipline) finds the still-armed `(token → release)` and calls `release(ptr)` exactly once. **Single owner, single free.** The `u64` result `0` is the natural "no handle" sentinel (also the cancellation sentinel).

**S-3 — streaming Call (the non-blocking primitive).** Do not hand Go a live lazy `FFI_ArrowArrayStream`; instead open a server-side cursor and let Go pull one batch per await:

```c
void sable_stream_open(const SableRuntime *rt, uint32_t op, const uint8_t *req, size_t req_len, uint64_t token); // → cursor handle
void sable_stream_next(const SableRuntime *rt, uint64_t cursor, uint64_t token);  // → *mut FFI_ArrowArray, 0 at end
void sable_stream_close(uint64_t cursor);  // drops the DataFusion stream; safe before end-of-stream to cancel
```

The handler returns a `Box<dyn Stream<Item = RecordBatch>>` stored in a cursor registry; each `_next` spawns a task that does `cursor.next().await`, exports one batch as a handle, and completes the token — the **multi-completion** capability sable's one-token-one-completion `Call` lacked. Per-batch handles reuse the S-2 net. Cancellation: `sable_stream_close` (or ctx-cancel) drops the stream mid-flight so IMBH releases the pinned snapshot, race-safe against an in-flight `_next`. The non-blocking property is only as good as the upstream stream's granularity (hence I-4a); the two residual quanta remain and belong to IMBH. Go surface: `stream.go` `OpenStream` / `Next` / `Close`; `Next` disarms via `sable_call_handle_taken` so there is no per-batch handle leak.

**S-4 — executor occupancy (guidance, not a work item).** The right answer to "a query monopolizes the single executor thread" is to make the query fully async via S-3, not to offload it. With S-3, each batch is a discrete task; between batches the executor returns to its run loop. `spawn_blocking` is demoted to an **optional, profile-driven scalpel for the cold-disk read** (better shaped as a small dedicated read pool applied inside the scan per batch, never the default). It is correctly **absent** from the delivered sable. Orthogonal throughput option: `new_multithread` (IO-disabled, `enable_time()`, no `enable_io()`) should create **zero epoll** while giving DataFusion cross-query parallelism; behind a `multithread` feature with an epoll-invariant test. Verify empirically (`make test-multithread`) before relying on the zero-epoll claim.

**S-5 — let the binding own the staticlib.** The binding needs a staticlib containing imbh + sable runtime + the registered handlers. sable's Go package builds with `-tags sable_extern_lib` to emit **no** `-lsable`/`-L` of its own (`sable/link_extern.go`), and sable's Rust crate is `["staticlib", "rlib"]` so the binding's combined crate depends on it as an `rlib` and absorbs its symbols. The C ABI stays additive so one header serves both. See Part IV §5.

**Minor sable-side nits (non-blocking):** the `multithread` epoll test is not in the default `make test` (needs `make test-multithread`); no stream-specific leak assertion yet (an open/drain/close ×N gate would harden `TestStreamEarlyClose`); a batch delivered by `Next()` but abandoned via `Close()` without taking sits in `HANDLES` until shutdown drain (freed, not leaked — fine under the one-goroutine-per-cursor contract).

**Later fix — the empty-result pointer (S-6, memory-safety).** The binding's concurrency-under-load gate surfaced a real bug in sable's byte-`Call` result ABI: `sable_call_result` returned `Vec::as_ptr()` for an **empty** `Vec<u8>`, which is the dangling-but-aligned sentinel `0x1` (align of `u8`). Stored into a GC-scanned Go pointer slot (imbh-go's `Rows` teardown calls `fetchQueryError` on every close, which returns empty bytes on a clean query), it crashed under a coincident GC stack scan — sub-page pointers are exactly what Go's stack scanner flags. Fixed both sides: Rust returns `core::ptr::null()` on empty; Go holds the result pointer as a non-scanned `uintptr` and materializes an `unsafe.Pointer` only when `n>0 && ptr!=0`. This is the memory-safety commit the binding pins at `545d04f`. General rule: a C-owned pointer must never live in a GC-scanned Go pointer variable.

---

# Part IV — The binding implementation plan (the glue)

A Go package `github.com/moriyoshi/imbh-go` that embeds IMBH and answers queries with zero-copy Arrow over sable. Both upstreams are done; this is the remaining work.

## 4.1 Architecture

```
github.com/moriyoshi/imbh-go            (Go package: DB, Rows, Query — later Ingest, typed queries)
   │ imports
   ├── github.com/moriyoshi/sable       (built -tags sable_extern_lib → contributes no -lsable)
   │      cgo: OpenStream / Next / Close  (the S-1..S-3 surface)
   └── arrow-go/v18 cdata               (zero-copy import of each FFI_ArrowArray batch)

CGO_LDFLAGS = -L<out> -limbhgo         ← ONE combined staticlib:

rust crate `imbhgo` (crate-type = ["staticlib"])   → libimbhgo.a  (~450 MB, cold build pulls DataFusion)
   ├── depends on  sable   (rlib)      → runtime + registry + all sable_* C symbols
   ├── depends on  imbh    (features: cdata, proto, search)
   └── imbhgo lib.rs: register_stream(OP_SQL, …) + FFI batch export + free shims
```

The combined `libimbhgo.a` retains all `sable_*` no_mangle symbols (verified by `nm`) — no `#[used]` shim needed. imbh and sable stay untouched; all binding code lives in `rust/` and the repo-root Go package.

## 4.2 The zero-copy batch handoff — the load-bearing two-free protocol

Each result batch crosses as one `FfiBatch { array: FFI_ArrowArray, schema: FFI_ArrowSchema }` — a `#[repr(C)]` struct, self-describing (schema in every batch, so no separate schema channel), behind a single `u64` pointer carried by sable's `Payload::Handle`.

**Export (Rust).** `StructArray::from(batch)` (zero-copy) → `arrow::ffi::to_ffi(&sa.into_data())` → box the `FfiBatch`, hand `Box::into_raw as u64` to `Payload::Handle(Handle::new(ptr, imbhgo_batch_release))`. (`into_data()` needs `use arrow::array::Array` in scope.)

**Import (Go).** `stream.Next()` (which already calls `sable_call_handle_taken` to disarm sable's net) → `cdata.ImportCRecordBatch(&fb.array, &fb.schema)` → `imbhgo_shell_free(ptr)`. The returned `arrow.RecordBatch`'s `Release()` later drives the C release that frees the Rust buffers.

**Why two free functions (the correctness argument):**

- **Taken path — `imbhgo_shell_free`** (`std::mem::forget` the inner FFI structs, free only the shell). Go's `cdata` import is a **move** — the returned `RecordBatch` owns the buffers and its `Release()` drives the C release — so Rust must not also release them. `forget` guarantees this **regardless of whether arrow-go nulls the source struct on import**, so correctness does not depend on arrow-go's move internals.
- **Abandoned path — `imbhgo_batch_release`** (full `Box` drop → the FFI structs' own `Drop` releases the still-live buffers, then the shell is freed). Used when a batch was buffered in sable's cursor channel and never imported (cursor closed early). This is the `Payload::Handle` release callback; sable's cursor-drain invokes it on exactly these.

Single owner, single free, on both paths. **This is the #1 thing to prove under test** (§4.6). Later optimization (deferred): cross the schema once at `OpenStream` and send bare `FFI_ArrowArray` batches imported via `ImportCRecordBatchWithSchema`.

## 4.3 The op protocol

Op ids are `uint32` constants shared between the Go package and `imbhgo` (a small hand-written table; no codegen initially).

| Op | Kind | Request bytes | Response | Status |
|----|------|---------------|----------|--------|
| `OP_SQL` (1) | stream (S-3) | `[8-byte LE db id][UTF-8 SQL]` | per-batch `FfiBatch` handles → end | ✅ M0 |
| `OP_INGEST_LOGS` (2) / `_TRACES` (3) / `_METRICS` (4) | byte `Call` | `[8-byte LE db id][OTLP export-request protobuf]` | 26-byte receipt | ✅ M2 |
| `OP_FLUSH` (5) | byte `Call` | `[8-byte LE db id]` | empty / error | ✅ M2 |
| `OP_QUERY_LOGS` (7) / `_METRICS` (8) / `_SPAN_METRICS` (9) | stream (S-3) | `[db id][query id][JSON query]` (native structs → JSON, **not** proto — see §4.4) | per-batch `FfiBatch` handles | ✅ M1 |
| `OP_QUERY_ERROR` (6) | byte `Call` | `[8-byte query id]` | terminal error bytes (empty = clean end) | ✅ M3 |

The op space has since grown to `1..33` — LGTM (10–12), `get_trace` (13), raw metric points (14), discovery/catalog/search/instant (15–22), admin ops (23–30), `LogPage` + its meta side-channel (31–32), and `log_count` (33). The **current** op table is the canonical one in `ARCHITECTURE.md` §7; this table records the design-era core.

- **Reads** run through `register_stream`: the handler calls `db.sql(&sql).stream().await?`, then `while let Some(b) = s.next().await { if tx.send(export_batch(b?)?).await.is_err() { break } }`. The `is_err()` break is the cancellation signal — Go's `Close()` drops the receiver, the `send` fails, the handler stops and drops the IMBH stream (releasing its pinned snapshot).
- **Ingest** stays on the copying byte `Call` — OTLP bytes are decoded immediately, so zero-copy-in buys nothing. Delivered in M2: four `sable::register` handlers (`OP_INGEST_*` → `db.ingest_otlp_*(&body).await`, `OP_FLUSH` → `db.flush().await`). The receipt is a fixed **26-byte LE record**: `accepted(u64) | rejected(u64) | lsn(u64) | durable(u8) | queued(u8)`. Because ingest is a byte `Call`, handler `Err` bytes surface as a Go `error` via sable's byte-Call error path — so ingest errors are reported properly (unlike the stream path, which still swallows errors until M3).
- **The `Db` handle.** `imbhgo` owns `HashMap<u64, Arc<imbh::Db>>` keyed by an opaque id; `imbhgo_open`/`_open_memory` insert, `imbhgo_close` removes. Every request (read or ingest) is prefixed with the 8-byte db id so the stateless handler recovers its `Db`. Opening is synchronous cgo, not a sable await.

## 4.4 Milestones

- **M0 — walking skeleton (open → SQL → zero-copy rows). ✅ DONE.** `imbhgo` crate (`imbhgo_open`/`_open_memory`/`_close`, db registry, `imbhgo_init` → `register_stream(OP_SQL, …)`, `export_batch`, the two free shims); Go `db.go` (`DB.Open`/`OpenInMemory`/`Close`, `DB.Query(sql) → Rows`, `Rows.Next` → `ImportCRecordBatch` + `shell_free`, `Rows.Close`; `init()` → `imbhgo_init` then `sable.Init`). Verified: combined lib builds, all symbols retained, tests for a constant `SELECT`, a multi-row `VALUES` query, and the early-close cancel path pass under `-race`; `gofmt`/`vet` clean. **Deferred to M1+**: M0 uses constant/`VALUES` SQL, so it exercises the transport + DataFusion stream but not the IMBH segment scan; real table queries need ingest (M2) first.
- **M2 — ingest. ✅ DONE.** `OP_INGEST_*` + `OP_FLUSH` byte ops (§4.3); Go `ingest.go` with `Receipt` + `DB.IngestOTLPLogs/Traces/Metrics([]byte) (Receipt, error)` and `DB.Flush() error`, all over `sable.Call`. Verified under `-race`: `TestIngestAndQuery` (Accepted=3 → buffer `count(*)=3` → `Flush` → segment `count(*)=3`), `TestQueryIngestedBodies` (`WHERE service='api'` → 2), `TestIngestBadBytes` (malformed OTLP → Go error). This is the first milestone exercising IMBH's I-4a lazy segment scan through the binding, and it proves data is queryable from the buffer immediately after ingest (no flush needed) and identically after `Flush()` moves it to a segment. New Go deps: `go.opentelemetry.io/proto/otlp`, `google.golang.org/protobuf`.
- **M1 — typed queries. ✅ DONE (with a prescription correction).** The design assumed proto (`imbh::proto::*Query` + `TryFrom`); the delivered binding uses **JSON** instead — the public Go API is hand-written structs regardless, and JSON handles optionals/maps with zero codegen (Go `encoding/json` ↔ Rust `serde_json`). Only **3** IMBH typed queries are Arrow-shaped (`logs().query_batches`, `metrics().range_batches`, `traces().span_metrics_batches`); they run via IMBH's **eager** `*_batches` (the lazy `to_sql`/`sql_with_params` are `pub(crate)`, unreachable externally) and stream out over the same `Rows`/`FfiBatch` path. Go: `DB.QueryLogs`/`QueryMetrics`/`QuerySpanMetrics` (raw Arrow) + Go-side decoders (`[]LogEntry` / `Matrix` / `[]SpanMetricPoint`). The non-row-shaped "typed struct" results mostly dissolved: computed metrics (`histogram_quantile`, range, instant) are reachable via SQL/UDFs today, and the rest are flat tables or Arrow-rows-plus-a-scalar, so "typed results" is a Go-side decode over the one Arrow transport, not a second serialization path.
- **M3 — errors & cancellation. ✅ DONE.** The stream wire has no error channel (a 0 handle only means "no batch"), so `OP_SQL` carries a Go-generated **query id** and a terminal error is stored keyed by that id and fetched via `OP_QUERY_ERROR`, returned from `Rows.Err()`. Cancellation uses sable's `NextCtx` (a per-pull `sable_call_cancel`), reporting `context.Canceled` — no sable change was needed. This query-id side-channel later generalized to carry the `LogPage` cursor and `QueryStats` (`OP_LOG_PAGE_META`).
- **M4 — hardening. ✅ DONE.** Backpressure (`SetMaxInFlight`/`TryIngest*` → `ErrBackpressure`/`RuntimeStats`; an open `Rows` holds an admission slot until `Close`, so the cap bounds live streams too). Two leak gates (§4.6): an in-process live-batch counter and a Valgrind buffer gate. Plus durability-reopen and concurrency-under-load gates — the latter surfaced a real **sable FFI memory-safety bug** (empty-result `Vec::as_ptr()` → the sub-page sentinel `0x1`, rejected by Go's GC stack scan), fixed both sides and captured by the `545d04f` pin. Deferred (not correctness): an Arrow-native `Rows` iterator and a sanitizer leak gate.
- **Beyond the milestones (all done).** LGTM languages (PromQL/LogQL/TraceQL over the Arrow-native `execute_*_batches` path); `LogPage` paging + `QueryStats`; `metrics().instant`, raw metric points, `traces().search`, discovery/catalog; read-only + `DbBuilder` opens; an admin/ops passthrough; `logs().count`; and the context-first API. **Externalization**: path deps replaced by crates.io imbh `0.1.0` + git-pinned sable (§4.5).

## 4.5 Build system

```toml
# rust/Cargo.toml (the imbhgo staticlib) — externalized (was path deps during M0..M4)
[lib]
crate-type = ["staticlib"]
[dependencies]
sable     = { git = "https://github.com/moriyoshi/sable", rev = "545d04f", default-features = false }  # rlib; no `demo`
imbh      = { version = "0.2.0", features = ["cdata", "proto", "search", "serde"] }  # crates.io
imbh-core = "0.2.0"                                               # direct + lockstep (canonical_json_object / to_hex)
imbh-lgtm = { version = "0.2.0", features = ["source"] }          # PromQL/LogQL/TraceQL
tokio   = { version = "1", features = ["rt", "sync"] }            # match imbh's tokio
futures = "0.3"                                                    # StreamExt for the producer loop
```

The three `imbh*` crates are pinned in **lockstep** at `0.2.0` so the direct `imbh-core` resolves to the same instance as imbh's transitive one (else `imbh::Attributes != imbh_core::Attributes` in the glue). sable is a **git dep** (not published) at the memory-safety-fix commit; the Go side uses a **direct** `require github.com/moriyoshi/sable <pseudo-version>` with **no** `replace` (a `replace` is ignored for downstream consumers). Local `../imbh` / `../sable` checkouts remain for co-development; re-pin (bump the version / `rev` + pseudo-version) rather than reverting to a path dep.

Go build: `#include "imbhgo.h"` (hand-written), compiled `-tags sable_extern_lib`; the `Makefile` builds `libimbhgo.a` then `CGO_LDFLAGS="-L$(PWD)/rust/target/release -limbhgo" go test -tags sable_extern_lib -race ./...`. sable's `link_extern.go` supplies base system libs. **Toolchain pins carry over**: `go.mod` mirrors sable's `go1.26.4` pin (ABI certified per `(Go × arch)`; sable so `sable.Init()` does not fail-closed) and the `-tags sable_portable` fallback. **Init ordering**: `imbhgo_init()` must register before `sable::Init`; it is idempotent (a `Once`) and called from the Go `init()` before any sable entry point. Explicit registration, not `#[ctor]` — deterministic.

## 4.6 Testing strategy

- **Correctness/equivalence (M0+):** every query result imported in Go must equal the same query run directly against IMBH. Run under `-race`.
- **The ownership gate (highest priority) — delivered two ways.** (1) An in-process live-batch counter (`imbhgo_live_batches`, plus the Db and query-error registry counters): N cycles of (a) full drain, (b) `Close()` with batches still buffered in the cursor, (c) a failing query leave the counter at 0 (balanced two-free: no leak, no double free) and drain the registries, with goroutines/fds back to baseline. (2) A **Valgrind buffer gate** (`make leak-valgrind`): the whole binary under Valgrind (Go `-tags valgrind` + `-tags sable_safe`), asserting zero libc-`malloc` definite-loss blocks — Rust's Arrow buffers use libc malloc, so this proves the *buffers* (not just the shells) are freed on both paths; the ~370 KB Valgrind reports lost is entirely Go-GC-heap false positives. This proves §4.2's two-free protocol. Run under `-race`. A sanitizer (ASAN/LSan) build (`RUSTFLAGS=-Zsanitizer=address`, nightly) remains a nice-to-have third gate.
- **Cancellation:** a long query `Close()`d after one batch drops the IMBH snapshot promptly (assert via segment-file refcount / that a subsequent `compact()` can unlink).
- **Backpressure:** producer faster than consumer stays bounded at `STREAM_BUF`.
- **Reuse sable's gates:** `-tags sable_safe` parity, `-tags sable_portable`, and `make test-multithread`'s single-epoll assertion with imbh linked in.

## 4.7 Risks and must-verifies

1. ~~**arrow-go `cdata` import semantics.**~~ **RETIRED** by the isolated prototype in [`proto-cdata/`](../../proto-cdata/) (passes under `-race`). Findings vs **arrow-go v18.5.1**: `ImportCRecordBatch(*CArrowArray, *CArrowSchema) (arrow.RecordBatch, error)` is a zero-copy **move** that **nulls the source array's `release`** on import (probe: armed before=true, after=false); the returned `RecordBatch.Release()` solely drives the C release. So the two-free protocol is validated (`shellFree`=forget on taken, `imbhgo_batch_release`=full drop on abandoned; 2000× loop, no race/crash). The `#[repr(C)]` `FfiBatch{array, schema}` layout round-trips. cgo cannot live in `_test.go` (Go forbids `import "C"` there), so the cgo bridge is in `bridge.go` and the test is pure Go. **Buffer-leak now proven** (not just no-crash): the Valgrind gate (`make leak-valgrind`, §4.6) confirms zero libc-`malloc` definite-loss blocks on both ownership paths — Rust's Arrow buffers use libc malloc (Valgrind-traceable), so a leaked buffer would show; a sanitizer (ASAN/LSan) build remains a nice-to-have third gate.
2. **Arrow version alignment.** The C Data Interface is ABI-stable across arrow versions, so Go's arrow need not match IMBH's Rust arrow (58.3.0); but confirm the C schema format strings IMBH emits are ones arrow-go parses (timestamps, `FixedSizeBinary` ids) — covered by the M0 equivalence test.
3. **`StructArray::from(RecordBatch)` + `to_ffi`** must round-trip IMBH's exact schemas (Utf8 not Utf8View — IMBH already reads Parquet as Utf8; good). Verify nested/dictionary columns if any table uses them.
4. **tokio/futures version skew.** `imbhgo`'s `tokio`/`futures` must be compatible with imbh's and sable's (one workspace lock).
5. **Error channel on the handle path.** S-2/S-3 deliver `0` for "no handle"; decide error surfacing in M3 — do not let a query error look like a clean end-of-stream. Distinguish `0 = end` from `0 = error` explicitly.
6. **Init/registration ordering.** A missed registration makes `OpenStream` return `ErrNoStream`; keep `imbhgo_init()` idempotent and called from Go `init()`.

---

# Part V — I-4a: the lazy per-batch segment scan (IMBH internal, done)

The concrete fix that made `Query::stream` genuinely lazy. Implemented in `../imbh` (`crates/imbh-query/src/provider.rs`); summarized here because it is the linchpin of the non-blocking property. It changes **execution only** — pushdown / pruning / `RowSelection` / bloom / `coerce` semantics are preserved bit-for-bit, and the eager `run_sql` collect path plus `QueryStats` keep working because draining the lazy stream to completion accrues the same stats.

**The approach.**
1. **Split reading from draining.** The old `read_segment` opened the Parquet reader **and drained it into a `Vec`**. Split it: `open_segment` keeps the open + bloom-prune + row-selection decisions (all cheap, no full body read) and returns the open `ParquetRecordBatchReader` itself rather than a drained `Vec`. (`ParquetRecordBatchReader` over `std::fs::File` is `Send`, so the stream holding it is `Send` as `SendableRecordBatchStream` requires.)
2. **A hand-rolled lazy `RecordBatchStream`.** Reads are synchronous, so the stream never returns `Poll::Pending` — a plain state machine (no `async-stream`) emits, in `poll_next`: (1) the mutable-buffer snapshot batch first (preserving the old buffer-∪-segments order), then (2) one batch per poll from the currently-open segment reader, then (3) open the next segment (bloom-prune, skipping pruned ones) and loop. Stats now accrue **during the drain** rather than up front — fine for both callers.
3. **Wrap as an `ExecutionPlan` via `StreamingTableExec`.** Implement `PartitionStream` and hand it to `StreamingTableExec` (which applies `projection` and `limit`), replacing the `MemTable` tail of `scan()`. `row_selection_for` still runs in `scan()` but only touches the Tantivy `.tidx`, not the Parquet body — the body read+decode is the only thing that moved into the stream. If `StreamingTableExec`'s constructor shape is awkward in the pinned DataFusion, fall back to a hand-rolled `ExecutionPlan` reusing the same `SegmentReadStream` verbatim.

**The acceptance gate (what closes I-4a).** `scan_reads_one_segment_per_poll`: build a DB with N segments, call `db.sql(q).stream().await?`, read **one** batch, and assert only the first segment's file has been opened (a test-only counter of segments opened = 1, not N); drain the rest and assert the counter reaches N and rows equal `collect()`. Under the old `MemTable` path the counter would already be N. **Verified passing** (0 read at plan / 1 after first poll / 3 after drain in the landed implementation, via `StreamingTableExec` over a `SegmentPartitionStream`/`SegmentBatchIter`). `limit` safety: it is always safe to ignore `limit` (over-producing is capped by the upstream limit enforcer; only under-producing would be wrong), so `StreamingTableExec`'s `LimitStream` early-stop is a perf nicety.

---

## Source map

The implementation this plan describes lives at: `rust/src/lib.rs` (the `imbhgo` staticlib), `db.go` (the Go package), `imbhgo.h` (the C ABI), `proto-cdata/` (the arrow-go ownership prototype), and `Makefile`. The as-built synthesis is in the root [`ARCHITECTURE.md`](../../ARCHITECTURE.md); the agent-facing orientation is in [`OVERVIEW.md`](./OVERVIEW.md).
