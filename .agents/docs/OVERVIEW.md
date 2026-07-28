# imbh-go Overview

## What this is

`github.com/moriyoshi/imbh-go` is a Go binding of **IMBH** (`../imbh`), a Rust embeddable observability database, using **sable** (`../sable`), a Rust-tokio ⇄ Go-scheduler fusion runtime, as the FFI transport. The goal is to let Go programs open an imbh database, ingest OTLP, run SQL / typed observability queries / PromQL·LogQL·TraceQL, and receive results as Arrow record batches with **zero-copy** data transfer across the language boundary.

## How it fits together

* **imbh** ... the query/storage engine (DataFusion-based). Async, streaming query API (`Db::sql(..).stream()`, `Query::stream`). Off-by-default `cdata` feature re-exports Arrow `FFI_*` types; `proto` and `search` features enable typed queries and full-text search; `serde` carries the paging cursor; the sibling `imbh-lgtm` crate (`source` feature) supplies PromQL/LogQL/TraceQL. imbh stays sable-agnostic and Go-agnostic.
* **sable** ... the FFI transport and runtime fusion. Provides a handler registry (`register` / `register_stream`), a byte `Call(op, req) -> resp` path, a bounded-mpsc streaming cursor (`sable_stream_open/next/close`), and a `Payload::Handle` mechanism that carries a bare `u64` (a pointer) across the boundary with an arm-before-complete release net. sable stays imbh-agnostic and Arrow-agnostic.
* **imbh-go (this repo)** ... the glue. A single combined Rust staticlib (`rust/`, crate `imbhgo`) depends on both upstreams as **external deps** (imbh from crates.io, sable git-pinned — see the Build model), registers stream handlers that drive imbh's `Query::stream` and export each batch via the Arrow C Data Interface (`arrow::ffi::to_ffi`), and exposes an `imbhgo_*` C ABI. The Go package wraps it, importing sable's Go package and re-hydrating each batch with `arrow-go/v18` `cdata.ImportCRecordBatch`.

## Build model

One combined `libimbhgo.a` (crate-type `staticlib`) fuses sable's runtime + imbh + the glue + the `imbhgo_*`/`sable_*` C ABI. The Go package is built with `-tags sable_extern_lib` so sable's Go package contributes no `-lsable`; the linker resolves every symbol against `libimbhgo.a`. `make` builds the Rust archive; `make test` builds it and runs `go test -tags sable_extern_lib -race ./...`. `go.mod` pins `toolchain go1.26.4` to match sable (the fused runtime reaches Go's internal ABI via `//go:linkname`).

Dependencies are external, so the binding builds without local checkouts: **imbh** from crates.io with `imbh`/`imbh-core`/`imbh-lgtm` pinned in lockstep at `0.1.1` (the shared version keeps `imbh-core` a single crate instance), and **sable** as a git dep at the `main` commit `0c6fe56` (carrying both the memory-safety fix and the Apple-target port) — Rust via `git`+`rev`, Go via a **direct** `require` (no `replace`, which downstream consumers ignore). Local `../imbh` / `../sable` checkouts remain the co-development path; re-pin when upstream changes land.

## Key design decisions

* **Fully async, streaming query path** ... not `spawn_blocking`. Each batch pull is a discrete completing task, so sable's single executor thread yields at a task boundary after every batch (cooperative interleaving); no query monopolizes it. This depends on imbh's lazy scan (I-4a) that yields one batch per `poll_next` rather than collecting all segments up front.
* **Zero-copy via Arrow C Data Interface** ... each result batch is an `FfiBatch { FFI_ArrowArray, FFI_ArrowSchema }` (`#[repr(C)]`, self-describing) behind one `u64` pointer carried by sable `Payload::Handle`.
* **Two-free ownership protocol** ... taken path `imbhgo_shell_free` (forget inner + free shell; Go's `Record` owns the buffers after a zero-copy move-import that NULLs the source) vs abandoned/buffered-at-close path `imbhgo_batch_release` (full `Box` drop → FFI `Drop` releases unconsumed buffers). This is robust regardless of arrow-go's null-on-move behavior.

## Status

The binding is functionally complete and hardened. Both upstreams (imbh I-1..I-4a, sable S-1..S-5 plus a later empty-result FFI memory-safety fix, pinned at `545d04f`) are done and verified. The Go surface, all under `-race`:

* **Lifecycle** ... `Open` / `OpenInMemory` / `OpenReadOnly` / `OpenWith(DbOptions)` / `Close`.
* **Ingest** ... `IngestOTLPLogs/Traces/Metrics` + `Flush`, with backpressure (`SetMaxInFlight`, `TryIngest*` → `ErrBackpressure`, `RuntimeStats`).
* **SQL** ... lazy, zero-copy Arrow (`Query(ctx, sql)`), with `Rows.Err()` error surfacing and context cancellation; computed results (`histogram_quantile`, `matches`, `json_get_str`) via IMBH's UDFs.
* **Typed queries** ... `QueryLogs`/`QueryMetrics`/`QuerySpanMetrics` (raw Arrow) + Go-side decoders (`[]LogEntry` / `Matrix` / `[]SpanMetricPoint`); `LogPage` cursor paging + `QueryStats`; `metrics().instant`, raw metric points, `traces().search`, discovery/catalog (`AttrNames`/`MetricCatalog`/`…`).
* **LGTM languages** ... `QueryPromQL`/`QueryLogQL`/`QueryTraceQL` over the same Arrow path (`execute_*_batches`) + `[]Series` / `[]TraceMatch` decoders.
* **Admin/ops** ... `Stats`/`Compact`/`Maintain`/`Snapshot`/`Segments`/`Export`. The whole query surface is context-first (`Foo(ctx, …)`).

Correctness is proven two independent ways — an in-process live-batch counter and a Valgrind buffer gate — plus durability-reopen and concurrency-under-load gates (the latter caught and fixed a real sable FFI memory-safety bug, the `545d04f` pin). **Remaining is infra only**: CI, an amd64 run, and re-pinning sable to a durable `main` commit. See `.agents/docs/JOURNAL.md` / `LTM/` for detail and `.agents/docs/TODO.md` for open items.

## Where to read more

The canonical, human-reader-ready architecture is `ARCHITECTURE.md` at the repository root — read it for component boundaries, the build model, the data path, the ownership protocol, and the source map. The deeper design rationale, the imbh/sable upstream prescriptions, and the milestone plan are consolidated in `.agents/docs/PLAN.md`. Durable agent-facing knowledge is consolidated under `.agents/docs/LTM/` (`INDEX.md` is the table of contents).
