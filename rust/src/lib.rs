//! imbhgo — the combined Rust staticlib for the IMBH Go binding (binding plan M0).
//!
//! It embeds `imbh::Db`, registers a streaming SQL handler on sable's runtime, and exports each
//! result batch to Go zero-copy via the Arrow C Data Interface (`FFI_ArrowArray`+`FFI_ArrowSchema`).
//! Ownership of exported batches follows the plan §2 two-free protocol (validated in `proto-cdata/`).
//!
//! C ABI (see `imbhgo.h`):
//!   imbhgo_init()                        register handlers (idempotent; call before sable Init)
//!   imbhgo_open_memory() -> u64          open an ephemeral in-memory Db, return its handle id
//!   imbhgo_open(ptr,len) -> u64          open an on-disk Db at a path, return its handle id (0=err)
//!   imbhgo_close(id)                     close/drop a Db handle
//!   imbhgo_shell_free(ptr)               free a taken batch's shell (arrays owned by Go's Record)
//! and the stream op OP_SQL, driven from Go via sable's OpenStream/Next/Close.

use std::collections::HashMap;
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::{Arc, LazyLock, Mutex, Once};

use futures::StreamExt;
use imbh::arrow::array::{
    Array, ArrayRef, BooleanArray, Float64Array, Int64Array, StringArray, StructArray,
};
use imbh::arrow::datatypes::{DataType, Field, Schema};
use imbh::arrow::ffi::to_ffi;
use imbh::arrow::record_batch::RecordBatch;
use imbh::{Db, FFI_ArrowArray, FFI_ArrowSchema};
use imbh_lgtm::{
    EvalLimits, EvalRange, FetchBounds, ImbhQueryModel, LogFetchRequest, LogStreamSchema,
    LogsSemanticsExt, MetricKind, MetricResolution, MetricsSemanticsExt, TracesSemanticsExt,
    TranslateContext, build_log_query as lgtm_log_query, translate_logql, translate_promql,
    translate_traceql,
};
use sable::{BatchSender, Handle, Payload};

/// Op ids (shared with the Go package). Every request is prefixed with an 8-byte little-endian Db
/// handle id; the remainder is op-specific.
///
/// Stream op (S-3): request = [id][UTF-8 SQL]; response = a stream of `FfiBatch` handles, 0 at end.
const OP_SQL: u32 = 1;
/// Byte ops (S-1 Call): request = [id][OTLP protobuf export-request bytes]; response = encoded receipt.
const OP_INGEST_LOGS: u32 = 2;
const OP_INGEST_TRACES: u32 = 3;
const OP_INGEST_METRICS: u32 = 4;
/// Byte op: request = [id]; flushes (seals) the buffer. Empty response on success.
const OP_FLUSH: u32 = 5;
/// Byte op: request = [8-byte query id]; returns the query's terminal error bytes (empty = clean end),
/// removing the entry. Go fetches this after a stream ends, since the S-3 wire has no error channel
/// (a 0 handle means only "no batch"). See [`OP_SQL`] request layout below.
const OP_QUERY_ERROR: u32 = 6;
/// Typed-query stream ops (M1): request = [db id][query id][JSON query]; response = FfiBatch handles,
/// like [`OP_SQL`]. The JSON maps onto imbh's typed builders (eager `*_batches` collect + stream out).
const OP_QUERY_LOGS: u32 = 7;
const OP_QUERY_METRICS: u32 = 8;
const OP_QUERY_SPAN_METRICS: u32 = 9;
/// LGTM-stack query languages (PromQL / LogQL / TraceQL). Stream ops: request = [db id][query id]
/// [JSON {query,start,end,step}]; results are mapped to Arrow and streamed like any other query.
/// Series (PromQL/LogQL) → columns `labels` (JSON), `timestamp` (ns), `value`; TraceQL → `trace_id`,
/// `span_id`.
const OP_PROMQL: u32 = 10;
const OP_LOGQL: u32 = 11;
const OP_TRACEQL: u32 = 12;
/// Fetch one trace's spans as Arrow. Request = [db id][query id][JSON {trace_id: "<hex>"}]; response =
/// the `SPAN_COLS` projection of that trace's spans. Pairs with TraceQL, which yields matching ids.
const OP_GET_TRACE: u32 = 13;
/// Raw metric samples as Arrow (`metrics().points_batches`). Request = [db id][query id][JSON
/// {metric,kind,filters,start,end,limit}]; response = the point projection (point_time, metric,
/// service, attributes, temporality, is_monotonic, then value | explicit_bounds+bucket_counts).
const OP_METRIC_POINTS: u32 = 14;
/// Metadata-discovery stream ops (read-only catalog surface). Each maps a flat `Vec<T>` from imbh's
/// async discovery API onto ONE Arrow batch, streamed out the shared FfiBatch path:
/// `OP_ATTR_NAMES` (`attrs().names`) → column `name`; `OP_ATTR_VALUES` (`attrs().values`, JSON
/// `{"key"}`) → column `value`; `OP_METRIC_CATALOG` (`metrics().catalog`) → `metric,unit,temporality,
/// kind`; `OP_METRIC_SERIES` (`metrics().series`, JSON `{"metric"}`) → column `labels` (canonical JSON).
/// `OP_METRIC_EXEMPLARS` (`metrics().exemplars`, JSON `{"metric"}`) → columns `time`, `value`,
/// `trace_id`/`span_id` (null when the exemplar carries none), `attributes`. `OP_LOG_VOLUME`
/// (`logs().volume_by`, JSON = the log-query wire plus `step_ns` and `group_by`) → columns
/// `bucket_time`, `labels` (canonical JSON of the label pairs), `count`.
const OP_ATTR_NAMES: u32 = 15;
const OP_ATTR_VALUES: u32 = 16;
const OP_METRIC_CATALOG: u32 = 17;
const OP_METRIC_SERIES: u32 = 18;
const OP_METRIC_EXEMPLARS: u32 = 19;
const OP_LOG_VOLUME: u32 = 20;
/// Trace search (`traces().search`, JSON = the trace-query wire) → one Arrow batch of trace summaries:
/// columns `trace_id` (Utf8 hex), `root_service`/`root_name` (Utf8, null when absent), `start_time`
/// (Int64), `duration_ns` (Int64), `span_count` (Int64), `error` (Boolean). No upstream Arrow form —
/// the `Vec<TraceSummary>` is mapped binding-side like the discovery handlers.
const OP_TRACE_SEARCH: u32 = 21;
/// Instant metric query (`metrics().instant`, JSON = the metric-query wire, same as `OP_QUERY_METRICS`)
/// → one Arrow batch shaped like the LGTM series batch: `labels` (Utf8 canonical JSON), `timestamp`
/// (Int64 ns), `value` (Float64). Exactly one row per series — the last sample of each range series
/// (Vector semantics).
const OP_METRIC_INSTANT: u32 = 22;
/// Admin / lifecycle byte ops (S-1 Call). Request = `[8-byte db id][JSON args]` (args empty for the
/// no-arg ops); response = JSON of a flat wire struct — the imbh return structs (`DbStats`,
/// `MaintenanceReport`, …) derive no serde, so each is mirrored field-by-field below — except
/// `OP_EXPORT`, which returns raw Arrow-IPC stream bytes. Writer-only ops (`maintain`/`compact`/
/// `snapshot`) surface imbh's read-only rejection as a Go error. See the `*_handler`s below.
const OP_STATS: u32 = 23;
const OP_MAINTAIN: u32 = 24;
const OP_COMPACT: u32 = 25;
const OP_SNAPSHOT: u32 = 26;
const OP_SEGMENTS: u32 = 27;
const OP_SEGMENT_FILES: u32 = 28;
const OP_DURABLE_THROUGH: u32 = 29;
const OP_EXPORT: u32 = 30;
/// Paged log query. `OP_LOG_PAGE` is a stream op (request = [db id][query id][JSON = the log-query wire
/// plus an opaque `after` cursor]); it streams the page's rows as Arrow like `OP_QUERY_LOGS`, and — via
/// `query_batches_with_stats` — stashes the page's `{next, stats}` for `OP_LOG_PAGE_META` to return.
/// `OP_LOG_PAGE_META` is a byte op (request = [8-byte query id]) that Go fetches after draining the
/// stream (mirroring `OP_QUERY_ERROR`'s out-of-band delivery, since the stream wire carries no scalars).
const OP_LOG_PAGE: u32 = 31;
const OP_LOG_PAGE_META: u32 = 32;

/// Log count. A byte op (request = [8-byte db id][JSON = the log-query wire]) that runs imbh's
/// `logs().count(filter)` — a `count(*)` over the filter, ignoring limit/direction — and returns the
/// total as an 8-byte little-endian `u64`. No streaming and no Arrow, so it stays off the two-free
/// batch-ownership path entirely (like the other byte ops).
const OP_LOG_COUNT: u32 = 33;

/// The `OP_SQL` request carries a Go-generated query id after the Db id so a terminal query error can
/// be reported back out-of-band: `[8-byte db id][8-byte query id][UTF-8 SQL]`.
static QUERY_ERRORS: LazyLock<Mutex<HashMap<u64, Vec<u8>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

/// Record a stream query's terminal error, keyed by its query id, for Go to fetch on end-of-stream.
fn store_query_error(query_id: u64, msg: &str) {
    QUERY_ERRORS
        .lock()
        .unwrap()
        .insert(query_id, msg.as_bytes().to_vec());
}

/// Record why an open failed under `err_id`, and return the 0 handle the ABI reports as "error".
///
/// The `imbhgo_open*` entry points are direct C calls, not sable ops, so they have only a `u64`
/// return with no room for a message — every failure used to collapse to `0` and Go had to invent
/// "open database at <path> failed", discarding the actual cause (wrong permissions, another writer
/// holding `writer.lock`, an unsupported platform). Go therefore passes a caller-allocated id, drawn
/// from the same counter as query ids so the two can share one slot map, and fetches the message
/// through the existing `OP_QUERY_ERROR` byte-Call. `err_id == 0` means the caller does not want it.
fn store_open_error(err_id: u64, e: impl std::fmt::Display) -> u64 {
    if err_id != 0 {
        store_query_error(err_id, &e.to_string());
    }
    0
}

/// Page metadata (`{next, stats}` JSON) for a paged log query, keyed by its query id. Stashed by the
/// paging stream handler before it streams rows; fetched (and removed) by `OP_LOG_PAGE_META` once Go
/// has drained the stream. Mirrors `QUERY_ERRORS`: the S-3 stream wire carries only batch handles, so
/// scalar page metadata rides this out-of-band byte-Call slot.
static PAGE_META: LazyLock<Mutex<HashMap<u64, Vec<u8>>>> =
    LazyLock::new(|| Mutex::new(HashMap::new()));

// --- Db handle registry: an opaque u64 id → Arc<imbh::Db>, so the stateless stream handler can
//     recover its Db from the id prefixed on each request. ------------------------------------------
static DBS: LazyLock<Mutex<HashMap<u64, Arc<Db>>>> = LazyLock::new(|| Mutex::new(HashMap::new()));
static DB_CTR: AtomicU64 = AtomicU64::new(1);

fn insert_db(db: Arc<Db>) -> u64 {
    let id = DB_CTR.fetch_add(1, Ordering::Relaxed);
    DBS.lock().unwrap().insert(id, db);
    id
}

fn lookup_db(id: u64) -> Option<Arc<Db>> {
    DBS.lock().unwrap().get(&id).cloned()
}

// --- Zero-copy batch handoff (plan §2). --------------------------------------------------------------

/// One result batch handed to Go: array + schema behind one pointer. `#[repr(C)]` so Go takes the
/// two fields at fixed offsets (array first).
#[repr(C)]
struct FfiBatch {
    array: FFI_ArrowArray,
    schema: FFI_ArrowSchema,
}

/// Live `FfiBatch` shells (created − freed). Every exported batch is freed exactly once via either
/// `imbhgo_shell_free` (taken) or `imbhgo_batch_release` (abandoned), so this returns to 0 once all
/// cursors are drained/closed. Exposed via `imbhgo_live_batches` for the leak gate; a non-zero value
/// after quiescence means a leaked shell, a negative value means a double free.
static LIVE_BATCHES: AtomicI64 = AtomicI64::new(0);

/// Export a `RecordBatch` as a boxed `FfiBatch`, returning `Payload::Handle` for sable to deliver.
/// `release` is the abandoned-path free (full drop) sable calls if Go never takes the batch.
fn export_batch(batch: imbh::arrow::record_batch::RecordBatch) -> Result<Payload, String> {
    let sa = StructArray::from(batch);
    let (array, schema) = to_ffi(&sa.into_data()).map_err(|e| e.to_string())?;
    let ptr = Box::into_raw(Box::new(FfiBatch { array, schema })) as u64;
    LIVE_BATCHES.fetch_add(1, Ordering::Relaxed);
    // sable's Handle carries the ptr + the abandoned-path release; its own Drop calls `release` if the
    // Payload is dropped without Go taking it (e.g. a buffered batch when the cursor is closed).
    Ok(Payload::Handle(Handle::new(ptr, imbhgo_batch_release)))
}

/// Taken path: Go imported the batch (its Record owns the buffers). Free ONLY the shell; `forget` the
/// FFI structs so their Drop does not double-release. (arrow-go nulls the source on import anyway —
/// see proto-cdata — but forget keeps both paths uniform and future-proof.)
///
/// # Safety
/// `ptr` must be a value previously handed to Go by this library for the taken path and not yet freed;
/// it is reconstructed into the owning box exactly once. Passing any other value, or calling twice with
/// the same `ptr`, is undefined behaviour.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn imbhgo_shell_free(ptr: u64) {
    if ptr != 0 {
        std::mem::forget(*unsafe { Box::from_raw(ptr as *mut FfiBatch) });
        LIVE_BATCHES.fetch_sub(1, Ordering::Relaxed);
    }
}

/// Abandoned path: a batch Go never imported (buffered when a cursor was closed). Full drop → the FFI
/// structs release the still-live buffers, then the shell is freed. This is the `Payload::Handle`
/// release callback.
unsafe extern "C" fn imbhgo_batch_release(ptr: u64) {
    if ptr != 0 {
        drop(unsafe { Box::from_raw(ptr as *mut FfiBatch) });
        LIVE_BATCHES.fetch_sub(1, Ordering::Relaxed);
    }
}

/// Test/observability accessors for the leak gate.
#[unsafe(no_mangle)]
pub extern "C" fn imbhgo_live_batches() -> i64 {
    LIVE_BATCHES.load(Ordering::Relaxed)
}

/// Count of Db handles currently open (registry size).
#[unsafe(no_mangle)]
pub extern "C" fn imbhgo_live_dbs() -> u64 {
    DBS.lock().unwrap().len() as u64
}

/// Count of un-fetched terminal query errors still held (should drain to 0 as Go fetches them).
#[unsafe(no_mangle)]
pub extern "C" fn imbhgo_pending_query_errors() -> u64 {
    QUERY_ERRORS.lock().unwrap().len() as u64
}

// --- The streaming SQL handler (S-3): parse [id][sql], run imbh's lazy stream, send each batch. -----

/// Byte-Call handler: return (and remove) a query's terminal error bytes; empty if it ended cleanly.
/// Go fetches this once its stream reports end-of-stream. Request = [8-byte query id].
async fn query_error_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    if req.len() < 8 {
        return Ok(Payload::Bytes(Vec::new()));
    }
    let query_id = u64::from_le_bytes(req[..8].try_into().unwrap());
    let err = QUERY_ERRORS
        .lock()
        .unwrap()
        .remove(&query_id)
        .unwrap_or_default();
    Ok(Payload::Bytes(err))
}

/// Parse the 8-byte Db id prefix, returning `(id, body)` where `body` is the remainder.
fn split_req(req: &[u8]) -> Option<(u64, &[u8])> {
    if req.len() < 8 {
        return None;
    }
    let id = u64::from_le_bytes(req[..8].try_into().ok()?);
    Some((id, &req[8..]))
}

/// Encode an `IngestReceipt` as a fixed 26-byte little-endian record the Go side decodes:
/// accepted(u64) | rejected(u64) | lsn(u64) | durable(u8) | queued(u8).
fn encode_receipt(r: &imbh::IngestReceipt) -> Vec<u8> {
    let mut b = Vec::with_capacity(26);
    b.extend_from_slice(&r.accepted.to_le_bytes());
    b.extend_from_slice(&r.rejected.to_le_bytes());
    // `lsn` is `Option<Lsn>` (Lsn = NonZero<u64>): None while queued for the async-ingest worker.
    // Encode absent as 0, which the Go side reads alongside the `queued` flag.
    b.extend_from_slice(&r.lsn.map(|l| l.get()).unwrap_or(0).to_le_bytes());
    b.push(r.durable as u8);
    b.push(r.is_queued() as u8);
    b
}

/// OTLP signal selector for the shared ingest handler.
#[derive(Clone, Copy)]
enum Signal {
    Logs,
    Traces,
    Metrics,
}

/// Byte-Call handler: ingest OTLP export-request bytes into the addressed Db, returning the encoded
/// receipt (or error bytes, which sable delivers as a Go error).
async fn ingest_handler(sig: Signal, req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, body) = split_req(&req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let receipt = match sig {
        Signal::Logs => db.ingest_otlp_logs(body).await,
        Signal::Traces => db.ingest_otlp_traces(body).await,
        Signal::Metrics => db.ingest_otlp_metrics(body).await,
    };
    receipt
        .map(|r| Payload::Bytes(encode_receipt(&r)))
        .map_err(|e| e.to_string().into_bytes())
}

/// Byte-Call handler: flush (seal) the addressed Db's buffer. Empty response on success.
async fn flush_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, _) = split_req(&req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    db.flush()
        .await
        .map(|_| Payload::Bytes(Vec::new()))
        .map_err(|e| e.to_string().into_bytes())
}

// --- Admin / lifecycle byte ops: map imbh's Db maintenance/introspection methods onto byte-`Call`
//     ops. imbh's return structs derive no serde, so each is mirrored into a serde wire struct and
//     serialized to JSON (or returned as raw Arrow-IPC bytes for `export`). ---------------------------

/// Parse `[8-byte db id][JSON args]`, deserializing the trailing body into `A` (empty body → default).
fn split_json_req<A: serde::de::DeserializeOwned + Default>(req: &[u8]) -> Result<(u64, A), Vec<u8>> {
    let (id, body) = split_req(req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let args = if body.is_empty() {
        A::default()
    } else {
        serde_json::from_slice(body).map_err(|e| format!("imbhgo: bad args: {e}").into_bytes())?
    };
    Ok((id, args))
}

/// Serialize a wire struct to a JSON `Payload::Bytes` (mapping a serialization failure to error bytes).
fn json_payload<T: serde::Serialize>(v: &T) -> Result<Payload, Vec<u8>> {
    serde_json::to_vec(v)
        .map(Payload::Bytes)
        .map_err(|e| e.to_string().into_bytes())
}

/// Resolve a table name (imbh's `Table::as_str` form, e.g. `"logs"`, `"metrics_gauge"`) to a `Table`.
/// `Table` has no `FromStr`, so we scan `Table::ALL`.
fn table_from_str(s: &str) -> Option<imbh::Table> {
    imbh::Table::ALL.iter().copied().find(|t| t.as_str() == s)
}

#[derive(serde::Serialize)]
struct TableStatsWire {
    table: String,
    segment_count: u64,
    segment_rows: u64,
    buffer_rows: u64,
    min_time_unix_nano: Option<i64>,
    max_time_unix_nano: Option<i64>,
}

#[derive(serde::Serialize)]
struct DbStatsWire {
    tables: Vec<TableStatsWire>,
    buffer_bytes: u64,
    wal_bytes: u64,
    durable_lsn: Option<u64>,
    ingest_queue_depth: u64,
    ingest_dropped: u64,
    ingest_errors: u64,
    /// imbh 0.5.0: points dropped by `Duplicates::Reject`. Stays 0 under every other policy.
    ingest_rejected: u64,
}

async fn stats_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, _) = split_req(&req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let s = db.stats().await.map_err(|e| e.to_string().into_bytes())?;
    let wire = DbStatsWire {
        tables: s
            .tables
            .iter()
            .map(|t| TableStatsWire {
                table: t.table.as_str().to_string(),
                segment_count: t.segment_count,
                segment_rows: t.segment_rows,
                buffer_rows: t.buffer_rows,
                min_time_unix_nano: t.min_time_unix_nano,
                max_time_unix_nano: t.max_time_unix_nano,
            })
            .collect(),
        buffer_bytes: s.buffer_bytes as u64,
        wal_bytes: s.wal_bytes,
        durable_lsn: s.durable_lsn.map(|l| l.get()),
        ingest_queue_depth: s.ingest_queue_depth as u64,
        ingest_dropped: s.ingest_dropped,
        ingest_errors: s.ingest_errors,
        ingest_rejected: s.ingest_rejected,
    };
    json_payload(&wire)
}

#[derive(serde::Serialize)]
struct MaintenanceReportWire {
    sealed: bool,
    segments_dropped: u64,
    bytes_freed: u64,
}

async fn maintain_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, _) = split_req(&req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let r = db.maintain().await.map_err(|e| e.to_string().into_bytes())?;
    json_payload(&MaintenanceReportWire {
        sealed: r.sealed,
        segments_dropped: r.segments_dropped,
        bytes_freed: r.bytes_freed,
    })
}

#[derive(serde::Serialize)]
struct CompactionReportWire {
    segments_merged: u64,
    segments_created: u64,
}

async fn compact_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, _) = split_req(&req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let r = db.compact().await.map_err(|e| e.to_string().into_bytes())?;
    json_payload(&CompactionReportWire {
        segments_merged: r.segments_merged,
        segments_created: r.segments_created,
    })
}

#[derive(serde::Deserialize, Default)]
struct SnapshotArgs {
    #[serde(default)]
    dir: String,
}

#[derive(serde::Serialize)]
struct SnapshotInfoWire {
    dir: String,
    segments: u64,
}

async fn snapshot_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, args): (u64, SnapshotArgs) = split_json_req(&req)?;
    if args.dir.is_empty() {
        return Err(b"imbhgo: snapshot requires a destination dir".to_vec());
    }
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let info = db
        .snapshot(&args.dir)
        .await
        .map_err(|e| e.to_string().into_bytes())?;
    json_payload(&SnapshotInfoWire {
        dir: info.dir.to_string_lossy().into_owned(),
        segments: info.segments,
    })
}

#[derive(serde::Serialize)]
struct SegmentRefWire {
    relative_path: String,
    min_time_unix_nano: i64,
    max_time_unix_nano: i64,
    rows: u64,
}

async fn segments_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, _) = split_req(&req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let wire: Vec<SegmentRefWire> = db
        .segments()
        .iter()
        .map(|s| SegmentRefWire {
            relative_path: s.relative_path.clone(),
            min_time_unix_nano: s.min_time_unix_nano,
            max_time_unix_nano: s.max_time_unix_nano,
            rows: s.rows,
        })
        .collect();
    json_payload(&wire)
}

#[derive(serde::Deserialize, Default)]
struct TableArg {
    #[serde(default)]
    table: String,
}

async fn segment_files_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, args): (u64, TableArg) = split_json_req(&req)?;
    let table = table_from_str(&args.table)
        .ok_or_else(|| format!("imbhgo: unknown table {:?}", args.table).into_bytes())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let files: Vec<String> = db
        .segment_files(table)
        .iter()
        .map(|p| p.to_string_lossy().into_owned())
        .collect();
    json_payload(&files)
}

#[derive(serde::Serialize)]
struct DurableWire {
    durable_lsn: Option<u64>,
}

async fn durable_through_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, _) = split_req(&req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let lsn = db.durable_through().await;
    json_payload(&DurableWire {
        durable_lsn: lsn.map(|l| l.get()),
    })
}

#[derive(serde::Deserialize, Default)]
struct ExportArgs {
    #[serde(default)]
    table: String,
    #[serde(default)]
    start: i64,
    #[serde(default)]
    end: i64,
}

async fn export_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (id, args): (u64, ExportArgs) = split_json_req(&req)?;
    let table = table_from_str(&args.table)
        .ok_or_else(|| format!("imbhgo: unknown table {:?}", args.table).into_bytes())?;
    let db = lookup_db(id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    // start==end==0 ⇒ the whole (half-open) time range; otherwise [start, end).
    let range = if args.start == 0 && args.end == 0 {
        imbh::TimeRange::all()
    } else {
        imbh::TimeRange::between(imbh::Timestamp(args.start), imbh::Timestamp(args.end))
    };
    // `export` takes `&Arc<Self>`; `db` is an `Arc<Db>`, so the method resolves by auto-ref.
    let bytes = db
        .export(table, range)
        .await
        .map_err(|e| e.to_string().into_bytes())?;
    Ok(Payload::Bytes(bytes))
}

/// Parse a stream request `[8-byte db id][8-byte query id][payload]` → `(db_id, query_id, payload)`.
fn split_stream_req(req: &[u8]) -> Option<(u64, u64, &[u8])> {
    if req.len() < 16 {
        return None;
    }
    let db_id = u64::from_le_bytes(req[..8].try_into().unwrap());
    let query_id = u64::from_le_bytes(req[8..16].try_into().unwrap());
    Some((db_id, query_id, &req[16..]))
}

async fn sql_stream_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return; // no query id to report against
    };
    let sql = match std::str::from_utf8(payload) {
        Ok(s) => s,
        Err(_) => return store_query_error(query_id, "invalid UTF-8 in SQL"),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let mut stream = match db.sql(sql).stream().await {
        Ok(s) => s,
        Err(e) => return store_query_error(query_id, &e.to_string()), // plan/open error
    };
    while let Some(item) = stream.next().await {
        let batch = match item {
            Ok(b) => b,
            Err(e) => return store_query_error(query_id, &e.to_string()), // mid-stream scan error
        };
        match export_batch(batch) {
            Ok(payload) => {
                // Go closed the cursor (receiver dropped) → stop; dropping `stream` releases imbh's
                // pinned snapshot. Not an error — Go initiated it.
                if tx.send(payload).await.is_err() {
                    return;
                }
            }
            Err(e) => return store_query_error(query_id, &e),
        }
    }
    // Clean end: nothing stored, so Go's OP_QUERY_ERROR fetch returns empty.
}

// --- Typed queries (M1): native JSON query structs → imbh's typed builders → eager `*_batches`,
//     streamed out through the same FfiBatch handle path. -------------------------------------------

/// Send an already-collected `Vec<RecordBatch>` out as the stream's batches (typed queries collect
/// eagerly via `*_batches`, then stream). Mirrors `sql_stream_handler`'s send loop.
async fn stream_batches(
    query_id: u64,
    batches: Vec<imbh::arrow::record_batch::RecordBatch>,
    tx: &BatchSender,
) {
    for batch in batches {
        match export_batch(batch) {
            Ok(payload) => {
                if tx.send(payload).await.is_err() {
                    return; // Go closed the cursor
                }
            }
            Err(e) => return store_query_error(query_id, &e),
        }
    }
}

/// Wire form of a log query (mirrors the Go `LogQuery`). Absent/zero fields are unset.
#[derive(serde::Deserialize, Default)]
struct LogQueryWire {
    #[serde(default)]
    service: String,
    #[serde(default, rename = "match")]
    text: String,
    #[serde(default)]
    attr_eq: std::collections::HashMap<String, String>,
    #[serde(default)]
    start: i64,
    #[serde(default)]
    end: i64,
    #[serde(default)]
    limit: i64,
    #[serde(default)]
    backward: bool,
    #[serde(default)]
    trace_id: String,
    #[serde(default)]
    span_id: String,
    #[serde(default)]
    severity_at_least: i64,
    #[serde(default)]
    attr_exists: Vec<String>,
    #[serde(default)]
    attr_matches: std::collections::HashMap<String, String>,
    #[serde(default)]
    attr_in: std::collections::HashMap<String, Vec<String>>,
    #[serde(default)]
    attr_not_in: std::collections::HashMap<String, Vec<String>>,
    #[serde(default)]
    attr_gt: std::collections::HashMap<String, f64>,
    #[serde(default)]
    attr_ge: std::collections::HashMap<String, f64>,
    #[serde(default)]
    attr_lt: std::collections::HashMap<String, f64>,
    #[serde(default)]
    attr_le: std::collections::HashMap<String, f64>,
    #[serde(default)]
    attr_regex: std::collections::HashMap<String, String>,
}

fn build_log_query(w: LogQueryWire) -> imbh::LogQuery {
    let mut q = imbh::LogQuery::new();
    if !w.service.is_empty() {
        q = q.service(&w.service);
    }
    if !w.text.is_empty() {
        q = q.matches(&w.text);
    }
    for (k, v) in &w.attr_eq {
        q = q.attr_eq(k, v);
    }
    // Trace correlation: skip silently if the hex fails to parse (don't panic).
    if !w.trace_id.is_empty() && let Some(id) = imbh::TraceId::from_hex(&w.trace_id) {
        q = q.trace_id(id);
    }
    if !w.span_id.is_empty() && let Some(id) = imbh::SpanId::from_hex(&w.span_id) {
        q = q.span_id(id);
    }
    if w.severity_at_least > 0 {
        q = q.severity_at_least(imbh::SeverityNumber(w.severity_at_least as u8));
    }
    for k in &w.attr_exists {
        q = q.attr_exists(k);
    }
    for (k, v) in &w.attr_matches {
        q = q.attr_matches(k, v);
    }
    for (k, vs) in &w.attr_in {
        let refs: Vec<&str> = vs.iter().map(String::as_str).collect();
        q = q.attr_in(k, &refs);
    }
    for (k, vs) in &w.attr_not_in {
        let refs: Vec<&str> = vs.iter().map(String::as_str).collect();
        q = q.attr_not_in(k, &refs);
    }
    for (k, n) in &w.attr_gt {
        q = q.attr_gt(k, *n);
    }
    for (k, n) in &w.attr_ge {
        q = q.attr_ge(k, *n);
    }
    for (k, n) in &w.attr_lt {
        q = q.attr_lt(k, *n);
    }
    for (k, n) in &w.attr_le {
        q = q.attr_le(k, *n);
    }
    for (k, pat) in &w.attr_regex {
        q = q.attr_regex(k, pat);
    }
    if w.start != 0 || w.end != 0 {
        q = q.range(imbh::TimeRange {
            start: imbh::Timestamp(w.start),
            end: imbh::Timestamp(w.end),
        });
    }
    if w.limit > 0 {
        q = q.limit(w.limit as usize);
    }
    q.direction(if w.backward {
        imbh::Direction::Backward
    } else {
        imbh::Direction::Forward
    })
}

async fn query_logs_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let w: LogQueryWire = match serde_json::from_slice(payload) {
        Ok(w) => w,
        Err(e) => return store_query_error(query_id, &format!("bad log query: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    // `query_batches` now returns just the batches (`query_batches_with_stats` keeps the QueryStats).
    match db.logs().query_batches(build_log_query(w)).await {
        Ok(batches) => stream_batches(query_id, batches, &tx).await,
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

/// Wire form of a paged log query: the log-query fields plus an opaque resume cursor. `after` is the
/// `next` token from a previous page (imbh's `PageCursor`, which serializes as a bare integer offset);
/// Go treats it as opaque and passes it back verbatim.
#[derive(serde::Deserialize, Default)]
struct LogPageWire {
    #[serde(flatten)]
    query: LogQueryWire,
    #[serde(default)]
    after: Option<serde_json::Value>,
}

/// Serde mirror of imbh's `QueryStats` (which is serde-gated; we keep an explicit wire so the JSON
/// field names are stable regardless of imbh's derive). `elapsed` (a `DurationNs`) becomes `elapsed_ns`.
#[derive(serde::Serialize)]
struct QueryStatsWire {
    segments_scanned: u64,
    segments_pruned: u64,
    rows_scanned: u64,
    rows_returned: u64,
    bytes_scanned: u64,
    elapsed_ns: u64,
    used_index: bool,
}

#[derive(serde::Serialize)]
struct LogPageMetaWire {
    /// Resume cursor for the next page: `Some` iff a full page was returned. Serialized as imbh's
    /// `PageCursor` format (a bare integer), which Go carries opaquely.
    next: Option<u64>,
    stats: QueryStatsWire,
}

/// Stash `{next, stats}` JSON for a paged log query, keyed by its query id, for `OP_LOG_PAGE_META`.
fn store_page_meta(query_id: u64, stats: &imbh::QueryStats, next: Option<u64>) {
    let wire = LogPageMetaWire {
        next,
        stats: QueryStatsWire {
            segments_scanned: stats.segments_scanned,
            segments_pruned: stats.segments_pruned,
            rows_scanned: stats.rows_scanned,
            rows_returned: stats.rows_returned,
            bytes_scanned: stats.bytes_scanned,
            elapsed_ns: stats.elapsed.0,
            used_index: stats.used_index,
        },
    };
    if let Ok(bytes) = serde_json::to_vec(&wire) {
        PAGE_META.lock().unwrap().insert(query_id, bytes);
    }
}

/// Byte-Call handler: return (and remove) a paged query's `{next, stats}` JSON; empty if none was
/// stashed (e.g. the query errored). Request = [8-byte query id]. Mirrors `query_error_handler`.
async fn log_page_meta_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    if req.len() < 8 {
        return Ok(Payload::Bytes(Vec::new()));
    }
    let query_id = u64::from_le_bytes(req[..8].try_into().unwrap());
    let meta = PAGE_META
        .lock()
        .unwrap()
        .remove(&query_id)
        .unwrap_or_default();
    Ok(Payload::Bytes(meta))
}

/// Byte-Call handler for [`OP_LOG_COUNT`]: decode the log-query wire, run `logs().count(filter)`, and
/// return the total as 8 little-endian bytes. Request = [8-byte db id][JSON log-query wire]. Errors
/// (bad JSON, unknown handle, engine error) come back as an `Err` byte string, which Go surfaces as an
/// `error` from `sable.CallCtx`.
async fn log_count_handler(req: Vec<u8>) -> Result<Payload, Vec<u8>> {
    let (db_id, body) = split_req(&req).ok_or_else(|| b"imbhgo: short request".to_vec())?;
    let w: LogQueryWire = serde_json::from_slice(body)
        .map_err(|e| format!("imbhgo: bad log count query: {e}").into_bytes())?;
    let db = lookup_db(db_id).ok_or_else(|| b"imbhgo: unknown db handle".to_vec())?;
    let n = db
        .logs()
        .count(build_log_query(w))
        .await
        .map_err(|e| e.to_string().into_bytes())?;
    Ok(Payload::Bytes(n.to_le_bytes().to_vec()))
}

async fn query_log_page_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let w: LogPageWire = match serde_json::from_slice(payload) {
        Ok(w) => w,
        Err(e) => return store_query_error(query_id, &format!("bad log page query: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let limit = w.query.limit;
    // Rows consumed before this page (imbh's PageCursor serializes as its inner usize / a bare number).
    let prev_offset: u64 = w.after.as_ref().and_then(|v| v.as_u64()).unwrap_or(0);
    let mut q = build_log_query(w.query);
    if let Some(tok) = w.after {
        match serde_json::from_value::<imbh::PageCursor>(tok) {
            Ok(c) => q = q.after(c),
            Err(e) => return store_query_error(query_id, &format!("bad page cursor: {e}")),
        }
    }
    match db.logs().query_batches_with_stats(q).await {
        Ok((batches, stats)) => {
            // imbh hands back a `next` cursor iff a full page was returned (rows_returned == limit),
            // pointing past the rows consumed (offset paging). We mirror that here since
            // `query_batches_with_stats` returns the stats but not the LogPage cursor. If imbh moves to
            // keyset paging (the docs flag this as possible), the cursor stops being an offset and this
            // derivation — plus the `after` round-trip above — must be revisited.
            let next = (limit > 0 && stats.rows_returned >= limit as u64)
                .then(|| prev_offset + stats.rows_returned);
            store_page_meta(query_id, &stats, next);
            stream_batches(query_id, batches, &tx).await;
        }
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

/// Wire form of a metric range query (mirrors the Go `MetricQuery`).
#[derive(serde::Deserialize, Default)]
struct MetricQueryWire {
    #[serde(default)]
    metric: String,
    #[serde(default)]
    sum: bool, // false = gauge, true = sum
    #[serde(default)]
    step: i64, // nanoseconds
    #[serde(default)]
    start: i64,
    #[serde(default)]
    end: i64,
    #[serde(default)]
    group_by: Vec<String>,
}

fn build_metric_query(w: MetricQueryWire) -> imbh::MetricQuery {
    let mut q = if w.sum {
        imbh::MetricQuery::sum(&w.metric)
    } else {
        imbh::MetricQuery::gauge(&w.metric)
    };
    if w.step > 0 {
        q = q.step(std::time::Duration::from_nanos(w.step as u64));
    }
    if w.start != 0 || w.end != 0 {
        q = q.range(imbh::TimeRange {
            start: imbh::Timestamp(w.start),
            end: imbh::Timestamp(w.end),
        });
    }
    for k in &w.group_by {
        q = q.group_by(k);
    }
    q
}

/// Wire form of a span-metrics (RED) query (mirrors the Go `SpanMetricsQuery`).
#[derive(serde::Deserialize, Default)]
struct SpanMetricsQueryWire {
    #[serde(default)]
    service: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    kind: String,
    #[serde(default)]
    status: String,
    #[serde(default)]
    group_by: Vec<String>,
    #[serde(default)]
    step: i64,
    #[serde(default)]
    start: i64,
    #[serde(default)]
    end: i64,
}

fn build_span_metrics_query(w: SpanMetricsQueryWire) -> imbh::SpanMetricsQuery {
    let mut q = imbh::SpanMetricsQuery::new();
    if !w.service.is_empty() {
        q = q.service(&w.service);
    }
    if !w.name.is_empty() {
        q = q.name(&w.name);
    }
    if !w.kind.is_empty() {
        q = q.kind(&w.kind);
    }
    if !w.status.is_empty() {
        q = q.status(&w.status);
    }
    for k in &w.group_by {
        q = q.group_by(k);
    }
    if w.step > 0 {
        q = q.step(std::time::Duration::from_nanos(w.step as u64));
    }
    if w.start != 0 || w.end != 0 {
        q = q.range(imbh::TimeRange {
            start: imbh::Timestamp(w.start),
            end: imbh::Timestamp(w.end),
        });
    }
    q
}

async fn query_span_metrics_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let w: SpanMetricsQueryWire = match serde_json::from_slice(payload) {
        Ok(w) => w,
        Err(e) => return store_query_error(query_id, &format!("bad span-metrics query: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    match db
        .traces()
        .span_metrics_batches(build_span_metrics_query(w))
        .await
    {
        Ok((batches, _stats)) => stream_batches(query_id, batches, &tx).await,
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

async fn query_metrics_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let w: MetricQueryWire = match serde_json::from_slice(payload) {
        Ok(w) => w,
        Err(e) => return store_query_error(query_id, &format!("bad metric query: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    match db.metrics().range_batches(build_metric_query(w)).await {
        Ok((batches, _stats)) => stream_batches(query_id, batches, &tx).await,
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

/// Instant metric query: `metrics().instant` yields a `Vector` (one `InstantSample` per series — the
/// last point of each range series). Map it onto ONE Arrow batch with the LGTM series-batch shape
/// (`labels`/`timestamp`/`value`) via the shared `series_batch` helper, stringifying each sample's
/// `(key, value)` label pairs through imbh's canonical JSON encoder (same form metric series use).
async fn metric_instant_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let w: MetricQueryWire = match serde_json::from_slice(payload) {
        Ok(w) => w,
        Err(e) => return store_query_error(query_id, &format!("bad metric query: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let vector = match db.metrics().instant(build_metric_query(w)).await {
        Ok(v) => v,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    let mut labels = Vec::with_capacity(vector.0.len());
    let mut ts = Vec::with_capacity(vector.0.len());
    let mut vals = Vec::with_capacity(vector.0.len());
    for s in vector.0 {
        let pairs: Vec<(String, imbh::AnyValue)> = s
            .labels
            .into_iter()
            .map(|(k, v)| (k, imbh::AnyValue::Str(v)))
            .collect();
        labels.push(imbh_core::canonical_json_object(&pairs));
        ts.push(s.sample.time.0);
        vals.push(s.sample.value);
    }
    match series_batch(labels, ts, vals) {
        Ok(batch) => stream_batches(query_id, vec![batch], &tx).await,
        Err(e) => store_query_error(query_id, &e),
    }
}

// --- LGTM query languages (PromQL / LogQL / TraceQL) → Arrow. --------------------------------------
//
// imbh-lgtm parses the query text and executes it against imbh::Db, returning evaluated typed results
// (Vec<PromSeries>, …). We map those to Arrow record batches so LGTM results ride the same zero-copy
// path as SQL and the typed queries — one transport, decodable Go-side into series. (The evaluation
// still materializes in Rust first; an Arrow-native execute path in imbh-lgtm would remove this remap.)

/// A JSON request common to all three LGTM ops: the query text plus the evaluation window.
#[derive(serde::Deserialize, Default)]
struct LgtmRequest {
    #[serde(default)]
    query: String,
    #[serde(default)]
    start: i64,
    #[serde(default)]
    end: i64,
    #[serde(default)]
    step: i64,
    /// Max log lines for a bare LogQL selector (ignored by the other forms).
    #[serde(default)]
    limit: i64,
}

/// Build the long-format series batch: `labels` (Utf8 JSON) | `timestamp` (Int64 ns) | `value`
/// (Float64), one row per sample across all series.
fn series_batch(labels: Vec<String>, ts: Vec<i64>, vals: Vec<f64>) -> Result<RecordBatch, String> {
    let schema = std::sync::Arc::new(Schema::new(vec![
        Field::new("labels", DataType::Utf8, false),
        Field::new("timestamp", DataType::Int64, false),
        Field::new("value", DataType::Float64, false),
    ]));
    RecordBatch::try_new(
        schema,
        vec![
            std::sync::Arc::new(StringArray::from(labels)) as ArrayRef,
            std::sync::Arc::new(Int64Array::from(ts)),
            std::sync::Arc::new(Float64Array::from(vals)),
        ],
    )
    .map_err(|e| e.to_string())
}

/// Build a PromQL translation context by resolving every stored metric: query-name is the metric with
/// dots→underscores (Prometheus convention), storage-name is the original, kind from the catalog.
async fn promql_context(db: &std::sync::Arc<Db>) -> Result<TranslateContext, String> {
    let catalog = db.metrics().catalog().await.map_err(|e| e.to_string())?;
    let metrics = catalog
        .into_iter()
        .map(|m| {
            let kind = match m.kind.as_str() {
                "sum" => MetricKind::CumulativeCounter,
                "histogram" => MetricKind::CumulativeHistogram,
                _ => MetricKind::Gauge,
            };
            MetricResolution {
                query_name: m.metric.replace('.', "_"),
                storage_name: m.metric,
                kind,
            }
        })
        .collect();
    Ok(TranslateContext { metrics })
}

async fn promql_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let r: LgtmRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => return store_query_error(query_id, &format!("bad promql request: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let ctx = match promql_context(&db).await {
        Ok(c) => c,
        Err(e) => return store_query_error(query_id, &format!("promql: {e}")),
    };
    let translated = match translate_promql(&r.query, &ctx) {
        Ok(t) => t,
        Err(d) => return store_query_error(query_id, &format!("promql: {}", d.message)),
    };
    let ImbhQueryModel::Prom(expr) = translated.model else {
        return store_query_error(query_id, "promql: not a metric expression");
    };
    let range = EvalRange {
        start_ns: r.start,
        end_ns: r.end,
        step_ns: r.step.max(1) as u64,
        lookback_ns: 300_000_000_000,
    };
    match db
        .metrics()
        .execute_promql_batches(&expr, range, EvalLimits::default())
        .await
    {
        Ok(batch) => stream_batches(query_id, vec![batch], &tx).await,
        Err(e) => store_query_error(query_id, &format!("promql: {e}")),
    }
}

async fn logql_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let r: LgtmRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => return store_query_error(query_id, &format!("bad logql request: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let translated = match translate_logql(&r.query, &TranslateContext::default()) {
        Ok(t) => t,
        Err(d) => return store_query_error(query_id, &format!("logql: {}", d.message)),
    };
    // LogQL has two result shapes, mirroring Loki: a bare selector yields log LINES (`streams`), a
    // range aggregation yields a metric SERIES (`matrix`). Dispatch on the translated model.
    let expr = match translated.model {
        ImbhQueryModel::LogSelector(filter) => {
            // Lines: turn the LogQL filter into IMBH's native LogQuery and stream the log rows.
            let bounds = match FetchBounds::new(r.start, r.end) {
                Ok(b) => b,
                Err(e) => return store_query_error(query_id, &format!("logql: {e}")),
            };
            let request = LogFetchRequest {
                bounds,
                filter,
                max_entries: if r.limit > 0 { r.limit as usize } else { 1000 },
            };
            let schema = LogStreamSchema::service_only();
            let q = match lgtm_log_query(&request, &schema) {
                Ok(q) => q,
                Err(e) => return store_query_error(query_id, &format!("logql: {e}")),
            };
            return match db.logs().query_batches(q).await {
                Ok(batches) => stream_batches(query_id, batches, &tx).await,
                Err(e) => store_query_error(query_id, &e.to_string()),
            };
        }
        ImbhQueryModel::Log(expr) => expr,
        _ => return store_query_error(query_id, "logql: translator returned a non-log model"),
    };
    let range = EvalRange {
        start_ns: r.start,
        end_ns: r.end,
        step_ns: r.step.max(1) as u64,
        lookback_ns: 0,
    };
    let schema = LogStreamSchema::service_only();
    match db
        .logs()
        .execute_logql_batches(&expr, range, EvalLimits::default(), &schema)
        .await
    {
        Ok(batch) => stream_batches(query_id, vec![batch], &tx).await,
        Err(e) => store_query_error(query_id, &format!("logql: {e}")),
    }
}

async fn traceql_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let r: LgtmRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => return store_query_error(query_id, &format!("bad traceql request: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let translated = match translate_traceql(&r.query, &TranslateContext::default()) {
        Ok(t) => t,
        Err(d) => return store_query_error(query_id, &format!("traceql: {}", d.message)),
    };
    let ImbhQueryModel::Trace(expr) = translated.model else {
        return store_query_error(query_id, "traceql: not a spanset expression");
    };
    let bounds = match FetchBounds::new(r.start, r.end) {
        Ok(b) => b,
        Err(e) => return store_query_error(query_id, &format!("traceql: {e}")),
    };
    match db
        .traces()
        .execute_traceql_batches(&expr, bounds, EvalLimits::default())
        .await
    {
        Ok(batch) => stream_batches(query_id, vec![batch], &tx).await,
        Err(e) => store_query_error(query_id, &format!("traceql: {e}")),
    }
}

/// Request for [`OP_GET_TRACE`].
#[derive(serde::Deserialize, Default)]
struct GetTraceRequest {
    #[serde(default)]
    trace_id: String,
}

/// Fetch one trace's spans as Arrow (`traces().get_batches`) — the zero-copy counterpart to
/// `traces().get`, and the natural follow-up to a TraceQL match.
async fn get_trace_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let r: GetTraceRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => return store_query_error(query_id, &format!("bad get-trace request: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let Some(trace_id) = imbh::TraceId::from_hex(&r.trace_id) else {
        return store_query_error(
            query_id,
            &format!("invalid trace id {:?} (want 32 hex chars)", r.trace_id),
        );
    };
    match db.traces().get_batches(trace_id).await {
        Ok(batches) => stream_batches(query_id, batches, &tx).await,
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

/// Request for [`OP_METRIC_POINTS`].
#[derive(serde::Deserialize, Default)]
struct MetricPointsWire {
    #[serde(default)]
    metric: String,
    /// "gauge" (default) | "sum" | "histogram".
    #[serde(default)]
    kind: String,
    /// Attribute equality filters (AND).
    #[serde(default)]
    filters: std::collections::HashMap<String, String>,
    #[serde(default)]
    start: i64,
    #[serde(default)]
    end: i64,
    #[serde(default)]
    limit: i64,
}

fn build_points_query(w: MetricPointsWire) -> imbh::MetricPointsQuery {
    let mut q = match w.kind.as_str() {
        "sum" => imbh::MetricPointsQuery::sum(w.metric.clone()),
        "histogram" => imbh::MetricPointsQuery::histogram(w.metric.clone()),
        _ => imbh::MetricPointsQuery::gauge(w.metric.clone()),
    };
    for (k, v) in &w.filters {
        q = q.filter(k.clone(), v.clone());
    }
    if w.start != 0 || w.end != 0 {
        q = q.range(imbh::TimeRange {
            start: imbh::Timestamp(w.start),
            end: imbh::Timestamp(w.end),
        });
    }
    if w.limit > 0 {
        q = q.limit(w.limit as usize);
    }
    q
}

/// Raw metric samples as Arrow — the unaggregated counterpart to the metric range query.
async fn metric_points_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let w: MetricPointsWire = match serde_json::from_slice(payload) {
        Ok(w) => w,
        Err(e) => return store_query_error(query_id, &format!("bad metric-points query: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    match db.metrics().points_batches(build_points_query(w)).await {
        Ok(batches) => stream_batches(query_id, batches, &tx).await,
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

// --- Metadata discovery (read-only catalog surface). ------------------------------------------------
//
// Each handler calls one of imbh's async discovery APIs, maps the flat `Vec<T>` it returns onto a
// single Arrow `RecordBatch`, and streams that out through the same `stream_batches`/`export_batch`
// path as every other query. No range/window — these are catalog lookups.

/// Build a one-column `Utf8` batch (`values` as the sole non-null column named `col`). Used by the
/// single-string discovery results (attribute names, attribute values, metric series labels).
fn utf8_batch(col: &str, values: Vec<String>) -> Result<RecordBatch, String> {
    let schema = std::sync::Arc::new(Schema::new(vec![Field::new(col, DataType::Utf8, false)]));
    RecordBatch::try_new(
        schema,
        vec![std::sync::Arc::new(StringArray::from(values)) as ArrayRef],
    )
    .map_err(|e| e.to_string())
}

/// Request payload for [`OP_ATTR_VALUES`].
#[derive(serde::Deserialize, Default)]
struct AttrValuesRequest {
    #[serde(default)]
    key: String,
}

/// Request payload for [`OP_METRIC_SERIES`].
#[derive(serde::Deserialize, Default)]
struct MetricSeriesRequest {
    #[serde(default)]
    metric: String,
}

/// All distinct attribute/label keys → column `name`.
async fn attr_names_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, _payload)) = split_stream_req(&req) else {
        return;
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    match db.attrs().names().await {
        Ok(names) => match utf8_batch("name", names) {
            Ok(batch) => stream_batches(query_id, vec![batch], &tx).await,
            Err(e) => store_query_error(query_id, &e),
        },
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

/// Distinct values for one attribute key → column `value`.
async fn attr_values_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let r: AttrValuesRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => return store_query_error(query_id, &format!("bad attr-values request: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    match db.attrs().values(&r.key).await {
        Ok(values) => match utf8_batch("value", values) {
            Ok(batch) => stream_batches(query_id, vec![batch], &tx).await,
            Err(e) => store_query_error(query_id, &e),
        },
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

/// The metric catalog → columns `metric`, `unit`, `temporality` (null when None), `kind`.
async fn metric_catalog_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, _payload)) = split_stream_req(&req) else {
        return;
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let catalog = match db.metrics().catalog().await {
        Ok(c) => c,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    let mut metrics = Vec::with_capacity(catalog.len());
    let mut units = Vec::with_capacity(catalog.len());
    let mut temporalities: Vec<Option<String>> = Vec::with_capacity(catalog.len());
    let mut kinds = Vec::with_capacity(catalog.len());
    for m in catalog {
        metrics.push(m.metric);
        units.push(m.unit);
        temporalities.push(m.temporality);
        kinds.push(m.kind);
    }
    let schema = std::sync::Arc::new(Schema::new(vec![
        Field::new("metric", DataType::Utf8, false),
        Field::new("unit", DataType::Utf8, false),
        Field::new("temporality", DataType::Utf8, true),
        Field::new("kind", DataType::Utf8, false),
    ]));
    let batch = match RecordBatch::try_new(
        schema,
        vec![
            std::sync::Arc::new(StringArray::from(metrics)) as ArrayRef,
            std::sync::Arc::new(StringArray::from(units)),
            std::sync::Arc::new(StringArray::from(temporalities)),
            std::sync::Arc::new(StringArray::from(kinds)),
        ],
    ) {
        Ok(b) => b,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    stream_batches(query_id, vec![batch], &tx).await;
}

/// The distinct label sets carrying a metric → column `labels`, one canonical-JSON string per series.
async fn metric_series_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let r: MetricSeriesRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => return store_query_error(query_id, &format!("bad metric-series request: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    match db.metrics().series(&r.metric).await {
        Ok(series) => {
            // Render each `Attributes` back to its canonical JSON via imbh's shared encoder (the same
            // byte-identical form `series` parsed the label set from), not a hand-rolled formatter.
            let labels: Vec<String> = series
                .iter()
                .map(|a| {
                    let pairs: Vec<(String, imbh::AnyValue)> =
                        a.iter().map(|(k, v)| (k.to_owned(), v.clone())).collect();
                    imbh_core::canonical_json_object(&pairs)
                })
                .collect();
            match utf8_batch("labels", labels) {
                Ok(batch) => stream_batches(query_id, vec![batch], &tx).await,
                Err(e) => store_query_error(query_id, &e),
            }
        }
        Err(e) => store_query_error(query_id, &e.to_string()),
    }
}

/// All exemplars recorded for a metric → columns `time` (Int64), `value` (Float64), `trace_id`
/// (Utf8, null when the exemplar has none), `span_id` (Utf8, null likewise), `attributes` (Utf8).
async fn metric_exemplars_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let r: MetricSeriesRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => return store_query_error(query_id, &format!("bad metric-exemplars request: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let exemplars = match db.metrics().exemplars(&r.metric).await {
        Ok(e) => e,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    let mut times = Vec::with_capacity(exemplars.len());
    let mut values = Vec::with_capacity(exemplars.len());
    let mut trace_ids: Vec<Option<String>> = Vec::with_capacity(exemplars.len());
    let mut span_ids: Vec<Option<String>> = Vec::with_capacity(exemplars.len());
    let mut attributes = Vec::with_capacity(exemplars.len());
    for ex in exemplars {
        times.push(ex.time.0);
        values.push(ex.value);
        trace_ids.push(ex.trace_id.map(|t| t.to_hex()));
        span_ids.push(ex.span_id.map(|s| s.to_hex()));
        attributes.push(ex.attributes);
    }
    let schema = std::sync::Arc::new(Schema::new(vec![
        Field::new("time", DataType::Int64, false),
        Field::new("value", DataType::Float64, false),
        Field::new("trace_id", DataType::Utf8, true),
        Field::new("span_id", DataType::Utf8, true),
        Field::new("attributes", DataType::Utf8, false),
    ]));
    let batch = match RecordBatch::try_new(
        schema,
        vec![
            std::sync::Arc::new(Int64Array::from(times)) as ArrayRef,
            std::sync::Arc::new(Float64Array::from(values)),
            std::sync::Arc::new(StringArray::from(trace_ids)),
            std::sync::Arc::new(StringArray::from(span_ids)),
            std::sync::Arc::new(StringArray::from(attributes)),
        ],
    ) {
        Ok(b) => b,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    stream_batches(query_id, vec![batch], &tx).await;
}

/// Request payload for [`OP_LOG_VOLUME`]: the log-query wire (flattened) plus the bucket width and the
/// optional group-by keys. `volume` is `volume_by` with an empty `group_by`.
#[derive(serde::Deserialize, Default)]
struct LogVolumeRequest {
    #[serde(flatten)]
    filter: LogQueryWire,
    #[serde(default)]
    step_ns: i64,
    #[serde(default)]
    group_by: Vec<String>,
}

/// Log volume per (step-bucket, label set) → columns `bucket_time` (Int64), `labels` (canonical JSON
/// of the bucket's `(key, value)` pairs — `{}` when un-grouped), `count` (Int64).
async fn log_volume_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let r: LogVolumeRequest = match serde_json::from_slice(payload) {
        Ok(r) => r,
        Err(e) => return store_query_error(query_id, &format!("bad log-volume request: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let step = std::time::Duration::from_nanos(r.step_ns.max(1) as u64);
    let group_refs: Vec<&str> = r.group_by.iter().map(String::as_str).collect();
    let buckets = match db
        .logs()
        .volume_by(build_log_query(r.filter), step, &group_refs)
        .await
    {
        Ok(b) => b,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    let mut times = Vec::with_capacity(buckets.len());
    let mut labels = Vec::with_capacity(buckets.len());
    let mut counts = Vec::with_capacity(buckets.len());
    for b in buckets {
        times.push(b.time.0);
        // Canonicalize the string label pairs via imbh's shared encoder (keys sorted, byte-stable) —
        // the same form metric series use, so Go decodes one canonical label-set string per row.
        let pairs: Vec<(String, imbh::AnyValue)> = b
            .labels
            .into_iter()
            .map(|(k, v)| (k, imbh::AnyValue::Str(v)))
            .collect();
        labels.push(imbh_core::canonical_json_object(&pairs));
        counts.push(b.count as i64);
    }
    let schema = std::sync::Arc::new(Schema::new(vec![
        Field::new("bucket_time", DataType::Int64, false),
        Field::new("labels", DataType::Utf8, false),
        Field::new("count", DataType::Int64, false),
    ]));
    let batch = match RecordBatch::try_new(
        schema,
        vec![
            std::sync::Arc::new(Int64Array::from(times)) as ArrayRef,
            std::sync::Arc::new(StringArray::from(labels)),
            std::sync::Arc::new(Int64Array::from(counts)),
        ],
    ) {
        Ok(b) => b,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    stream_batches(query_id, vec![batch], &tx).await;
}

/// Wire form of a trace search query (mirrors the Go `TraceQuery`). All fields default so an empty
/// object is a valid "match everything" query; each is applied only when non-empty/non-zero.
#[derive(serde::Deserialize, Default)]
struct TraceQueryWire {
    #[serde(default)]
    service: String,
    #[serde(default)]
    name: String,
    #[serde(default)]
    status: String,
    #[serde(default)]
    kind: String,
    #[serde(default)]
    min_duration_ns: i64,
    #[serde(default)]
    max_duration_ns: i64,
    #[serde(default)]
    attr_eq: std::collections::HashMap<String, String>,
    #[serde(default)]
    attr_exists: Vec<String>,
    #[serde(default)]
    attr_matches: std::collections::HashMap<String, String>,
    #[serde(default)]
    attr_in: std::collections::HashMap<String, Vec<String>>,
    #[serde(default)]
    attr_not_in: std::collections::HashMap<String, Vec<String>>,
    #[serde(default)]
    attr_gt: std::collections::HashMap<String, f64>,
    #[serde(default)]
    attr_ge: std::collections::HashMap<String, f64>,
    #[serde(default)]
    attr_lt: std::collections::HashMap<String, f64>,
    #[serde(default)]
    attr_le: std::collections::HashMap<String, f64>,
    #[serde(default)]
    attr_regex: std::collections::HashMap<String, String>,
    #[serde(default)]
    start: i64,
    #[serde(default)]
    end: i64,
    #[serde(default)]
    limit: i64,
}

fn build_trace_query(w: TraceQueryWire) -> imbh::TraceQuery {
    let mut q = imbh::TraceQuery::new();
    if !w.service.is_empty() {
        q = q.service(&w.service);
    }
    if !w.name.is_empty() {
        q = q.name(&w.name);
    }
    if !w.status.is_empty() {
        q = q.status(&w.status);
    }
    if !w.kind.is_empty() {
        q = q.kind(&w.kind);
    }
    if w.min_duration_ns > 0 {
        q = q.min_duration(std::time::Duration::from_nanos(w.min_duration_ns as u64));
    }
    if w.max_duration_ns > 0 {
        q = q.max_duration(std::time::Duration::from_nanos(w.max_duration_ns as u64));
    }
    for (k, v) in &w.attr_eq {
        q = q.attr_eq(k, v);
    }
    for k in &w.attr_exists {
        q = q.attr_exists(k);
    }
    for (k, v) in &w.attr_matches {
        q = q.attr_matches(k, v);
    }
    for (k, vs) in &w.attr_in {
        let refs: Vec<&str> = vs.iter().map(String::as_str).collect();
        q = q.attr_in(k, &refs);
    }
    for (k, vs) in &w.attr_not_in {
        let refs: Vec<&str> = vs.iter().map(String::as_str).collect();
        q = q.attr_not_in(k, &refs);
    }
    for (k, n) in &w.attr_gt {
        q = q.attr_gt(k, *n);
    }
    for (k, n) in &w.attr_ge {
        q = q.attr_ge(k, *n);
    }
    for (k, n) in &w.attr_lt {
        q = q.attr_lt(k, *n);
    }
    for (k, n) in &w.attr_le {
        q = q.attr_le(k, *n);
    }
    for (k, pat) in &w.attr_regex {
        q = q.attr_regex(k, pat);
    }
    if w.start != 0 || w.end != 0 {
        q = q.range(imbh::TimeRange {
            start: imbh::Timestamp(w.start),
            end: imbh::Timestamp(w.end),
        });
    }
    if w.limit > 0 {
        q = q.limit(w.limit as usize);
    }
    q
}

/// Trace search → one Arrow batch of trace summaries. Maps the upstream `Vec<TraceSummary>` (no Arrow
/// form exists upstream) onto columns `trace_id` (Utf8 hex), `root_service`/`root_name` (Utf8, SQL-NULL
/// when the root span carries none), `start_time`/`duration_ns`/`span_count` (Int64), `error` (Boolean).
/// An empty result still emits the empty-but-typed batch, matching the discovery handlers.
async fn trace_search_handler(req: Vec<u8>, tx: BatchSender) {
    let Some((db_id, query_id, payload)) = split_stream_req(&req) else {
        return;
    };
    let w: TraceQueryWire = match serde_json::from_slice(payload) {
        Ok(w) => w,
        Err(e) => return store_query_error(query_id, &format!("bad trace query: {e}")),
    };
    let Some(db) = lookup_db(db_id) else {
        return store_query_error(query_id, "unknown db handle");
    };
    let summaries = match db.traces().search(build_trace_query(w)).await {
        Ok(s) => s,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    let mut trace_ids = Vec::with_capacity(summaries.len());
    let mut root_services: Vec<Option<String>> = Vec::with_capacity(summaries.len());
    let mut root_names: Vec<Option<String>> = Vec::with_capacity(summaries.len());
    let mut start_times = Vec::with_capacity(summaries.len());
    let mut durations = Vec::with_capacity(summaries.len());
    let mut span_counts = Vec::with_capacity(summaries.len());
    let mut errors = Vec::with_capacity(summaries.len());
    for s in summaries {
        trace_ids.push(s.trace_id.to_hex());
        root_services.push(s.root_service);
        root_names.push(s.root_name);
        start_times.push(s.start_time.0);
        durations.push(s.duration_ns.0 as i64);
        span_counts.push(s.span_count as i64);
        errors.push(s.error);
    }
    let schema = std::sync::Arc::new(Schema::new(vec![
        Field::new("trace_id", DataType::Utf8, false),
        Field::new("root_service", DataType::Utf8, true),
        Field::new("root_name", DataType::Utf8, true),
        Field::new("start_time", DataType::Int64, false),
        Field::new("duration_ns", DataType::Int64, false),
        Field::new("span_count", DataType::Int64, false),
        Field::new("error", DataType::Boolean, false),
    ]));
    let batch = match RecordBatch::try_new(
        schema,
        vec![
            std::sync::Arc::new(StringArray::from(trace_ids)) as ArrayRef,
            std::sync::Arc::new(StringArray::from(root_services)),
            std::sync::Arc::new(StringArray::from(root_names)),
            std::sync::Arc::new(Int64Array::from(start_times)),
            std::sync::Arc::new(Int64Array::from(durations)),
            std::sync::Arc::new(Int64Array::from(span_counts)),
            std::sync::Arc::new(BooleanArray::from(errors)),
        ],
    ) {
        Ok(b) => b,
        Err(e) => return store_query_error(query_id, &e.to_string()),
    };
    stream_batches(query_id, vec![batch], &tx).await;
}

// --- Lifecycle FFI. ---------------------------------------------------------------------------------

/// Register handlers on sable. Idempotent; call once before any sable entry point.
#[unsafe(no_mangle)]
pub extern "C" fn imbhgo_init() {
    static ONCE: Once = Once::new();
    ONCE.call_once(|| {
        sable::register_stream(OP_SQL, |req, tx| async move { sql_stream_handler(req, tx).await });
        sable::register(OP_INGEST_LOGS, |req| async move {
            ingest_handler(Signal::Logs, req).await
        });
        sable::register(OP_INGEST_TRACES, |req| async move {
            ingest_handler(Signal::Traces, req).await
        });
        sable::register(OP_INGEST_METRICS, |req| async move {
            ingest_handler(Signal::Metrics, req).await
        });
        sable::register(OP_FLUSH, |req| async move { flush_handler(req).await });
        sable::register(OP_QUERY_ERROR, |req| async move { query_error_handler(req).await });
        sable::register(OP_STATS, |req| async move { stats_handler(req).await });
        sable::register(OP_MAINTAIN, |req| async move { maintain_handler(req).await });
        sable::register(OP_COMPACT, |req| async move { compact_handler(req).await });
        sable::register(OP_SNAPSHOT, |req| async move { snapshot_handler(req).await });
        sable::register(OP_SEGMENTS, |req| async move { segments_handler(req).await });
        sable::register(OP_SEGMENT_FILES, |req| async move {
            segment_files_handler(req).await
        });
        sable::register(OP_DURABLE_THROUGH, |req| async move {
            durable_through_handler(req).await
        });
        sable::register(OP_EXPORT, |req| async move { export_handler(req).await });
        sable::register_stream(OP_LOG_PAGE, |req, tx| async move {
            query_log_page_handler(req, tx).await
        });
        sable::register(OP_LOG_PAGE_META, |req| async move {
            log_page_meta_handler(req).await
        });
        sable::register(OP_LOG_COUNT, |req| async move { log_count_handler(req).await });
        sable::register_stream(OP_QUERY_LOGS, |req, tx| async move {
            query_logs_handler(req, tx).await
        });
        sable::register_stream(OP_QUERY_METRICS, |req, tx| async move {
            query_metrics_handler(req, tx).await
        });
        sable::register_stream(OP_QUERY_SPAN_METRICS, |req, tx| async move {
            query_span_metrics_handler(req, tx).await
        });
        sable::register_stream(OP_PROMQL, |req, tx| async move { promql_handler(req, tx).await });
        sable::register_stream(OP_LOGQL, |req, tx| async move { logql_handler(req, tx).await });
        sable::register_stream(OP_TRACEQL, |req, tx| async move { traceql_handler(req, tx).await });
        sable::register_stream(OP_GET_TRACE, |req, tx| async move {
            get_trace_handler(req, tx).await
        });
        sable::register_stream(OP_METRIC_POINTS, |req, tx| async move {
            metric_points_handler(req, tx).await
        });
        sable::register_stream(OP_ATTR_NAMES, |req, tx| async move {
            attr_names_handler(req, tx).await
        });
        sable::register_stream(OP_ATTR_VALUES, |req, tx| async move {
            attr_values_handler(req, tx).await
        });
        sable::register_stream(OP_METRIC_CATALOG, |req, tx| async move {
            metric_catalog_handler(req, tx).await
        });
        sable::register_stream(OP_METRIC_SERIES, |req, tx| async move {
            metric_series_handler(req, tx).await
        });
        sable::register_stream(OP_METRIC_EXEMPLARS, |req, tx| async move {
            metric_exemplars_handler(req, tx).await
        });
        sable::register_stream(OP_LOG_VOLUME, |req, tx| async move {
            log_volume_handler(req, tx).await
        });
        sable::register_stream(OP_TRACE_SEARCH, |req, tx| async move {
            trace_search_handler(req, tx).await
        });
        sable::register_stream(OP_METRIC_INSTANT, |req, tx| async move {
            metric_instant_handler(req, tx).await
        });
    });
}

/// Open an ephemeral in-memory Db; returns its handle id (0 on error, with the cause recorded under
/// `err_id` — see [`store_open_error`]).
#[unsafe(no_mangle)]
pub extern "C" fn imbhgo_open_memory(err_id: u64) -> u64 {
    match Db::in_memory().open() {
        Ok(db) => insert_db(db),
        Err(e) => store_open_error(err_id, e),
    }
}

/// Open an on-disk Db at the UTF-8 path `[ptr,len)`; returns its handle id (0 on error, with the
/// cause recorded under `err_id` — see [`store_open_error`]).
///
/// # Safety
/// `ptr` must point to at least `len` initialized bytes for the duration of the call (a valid UTF-8
/// path buffer owned by the caller). The bytes are only read, not retained past return.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn imbhgo_open(ptr: *const u8, len: usize, err_id: u64) -> u64 {
    let path = match std::str::from_utf8(unsafe { std::slice::from_raw_parts(ptr, len) }) {
        Ok(p) => p,
        Err(e) => return store_open_error(err_id, format_args!("path is not valid UTF-8: {e}")),
    };
    match Db::builder(path).open() {
        Ok(db) => insert_db(db),
        Err(e) => store_open_error(err_id, e),
    }
}

/// Open an existing on-disk Db **read-only** at the UTF-8 path `[ptr,len)`; returns its handle id
/// (0 on error). Takes no writer lock, so it coexists with the single writer and other readers
/// (imbh: `Db::open_read_only`). Rejected (→ 0) if the writer had its WAL off; use the options open
/// with `allow_stale_reads` to accept seal-interval freshness in that case.
///
/// # Safety
/// `ptr` must point to at least `len` initialized bytes for the duration of the call (a valid UTF-8
/// path buffer owned by the caller). The bytes are only read, not retained past return.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn imbhgo_open_read_only(ptr: *const u8, len: usize, err_id: u64) -> u64 {
    let path = match std::str::from_utf8(unsafe { std::slice::from_raw_parts(ptr, len) }) {
        Ok(p) => p,
        Err(e) => return store_open_error(err_id, format_args!("path is not valid UTF-8: {e}")),
    };
    match Db::open_read_only(path) {
        Ok(db) => insert_db(db),
        Err(e) => store_open_error(err_id, e),
    }
}

/// Open a Db with builder options serialized as JSON at `[ptr,len)` (see `DbOptionsWire` for the
/// fields; `path` is required). Returns its handle id (0 on error). Maps the JSON onto imbh's
/// `DbBuilder` setters. The host-runtime option variants (`Maintenance::Runtime`, `Ingest::Async`),
/// which carry a tokio `Handle`, are intentionally not exposed — they need explicit runtime wiring.
/// A malformed `flush` or `duplicates` spec fails the open (see `build_db`); every other tag falls
/// back to the default.
///
/// # Safety
/// `ptr` must point to at least `len` initialized bytes for the duration of the call (a valid UTF-8
/// JSON buffer owned by the caller). The bytes are only read, not retained past return.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn imbhgo_open_opts(ptr: *const u8, len: usize, err_id: u64) -> u64 {
    let json = unsafe { std::slice::from_raw_parts(ptr, len) };
    let w: DbOptionsWire = match serde_json::from_slice(json) {
        Ok(w) => w,
        Err(e) => return store_open_error(err_id, format_args!("bad options JSON: {e}")),
    };
    match build_db(w) {
        Ok(db) => insert_db(db),
        Err(e) => store_open_error(err_id, e),
    }
}

/// JSON wire for `imbhgo_open_opts`: a flat mirror of the `DbBuilder` setters. Absent/zero/empty
/// fields leave imbh's default in place. `compression`/`wal_mode`/`refresh` are string tags
/// (unrecognized values are ignored, keeping the default); `flush` and `duplicates` are spec strings
/// parsed by imbh, where a malformed value is an error rather than a silent fallback.
#[derive(serde::Deserialize, Default)]
struct DbOptionsWire {
    #[serde(default)]
    path: String,
    #[serde(default)]
    read_only: bool,
    #[serde(default)]
    allow_stale_reads: bool,
    #[serde(default)]
    memory_budget_bytes: u64,
    #[serde(default)]
    compression: String, // "none" | "lz4" | "zstd"
    #[serde(default)]
    zstd_level: i32,
    #[serde(default)]
    wal_mode: String, // "off" | "always" | "interval"
    #[serde(default)]
    wal_interval_ns: i64,
    #[serde(default)]
    retention_days: u64,
    #[serde(default)]
    max_disk_bytes: u64,
    #[serde(default)]
    refresh: String, // "onquery" | "manual" | "ttl"
    #[serde(default)]
    refresh_ttl_ns: i64,
    #[serde(default)]
    maintenance_background_ns: i64,
    #[serde(default)]
    flush: String, // imbh FlushPolicy spec, e.g. "interval=5s,wal=64MiB" or "manual"
    #[serde(default)]
    duplicates: String, // imbh Duplicates spec: "error_on_read" | "last_wins" | "reject[,recent=N]"
    #[serde(default)]
    promote_keys: Vec<String>,
}

/// Build and open a Db from `DbOptionsWire`, applying only the options the caller set.
fn build_db(w: DbOptionsWire) -> imbh::Result<Arc<Db>> {
    let mut b = Db::builder(&w.path);
    if w.read_only {
        b = b.access(imbh::Access::ReadOnly);
    }
    if w.allow_stale_reads {
        b = b.allow_stale_reads();
    }
    if w.memory_budget_bytes > 0 {
        b = b.memory_budget(imbh::MemoryBudget::total(w.memory_budget_bytes as usize));
    }
    match w.compression.as_str() {
        "none" => b = b.compression(imbh::Compression::None),
        "lz4" => b = b.compression(imbh::Compression::Lz4),
        "zstd" => b = b.compression(imbh::Compression::Zstd(w.zstd_level)),
        _ => {}
    }
    match w.wal_mode.as_str() {
        "off" => b = b.wal(imbh::WalMode::Off),
        "always" => b = b.wal(imbh::WalMode::Always),
        "interval" => {
            b = b.wal(imbh::WalMode::Interval(std::time::Duration::from_nanos(
                w.wal_interval_ns as u64,
            )))
        }
        _ => {}
    }
    if w.retention_days > 0 || w.max_disk_bytes > 0 {
        let mut r = if w.retention_days > 0 {
            imbh::Retention::days(w.retention_days)
        } else {
            imbh::Retention::none()
        };
        if w.max_disk_bytes > 0 {
            r = r.max_disk_bytes(w.max_disk_bytes);
        }
        b = b.retention(r);
    }
    match w.refresh.as_str() {
        "onquery" => b = b.refresh(imbh::Refresh::OnQuery),
        "manual" => b = b.refresh(imbh::Refresh::Manual),
        "ttl" => {
            b = b.refresh(imbh::Refresh::Ttl(std::time::Duration::from_nanos(
                w.refresh_ttl_ns as u64,
            )))
        }
        _ => {}
    }
    if w.maintenance_background_ns > 0 {
        b = b.maintenance(imbh::Maintenance::Background(std::time::Duration::from_nanos(
            w.maintenance_background_ns as u64,
        )));
    }
    // Only set a policy when the caller spelled one: leaving `DbBuilder::flush` unset is *not* the
    // same as setting `FlushPolicy::default()`. Unset resolves to `default().or_interval(maintenance
    // interval)` (the historical "seal on the maintenance tick" behavior); an explicit default has no
    // periodic trigger at all. A malformed spec fails the open rather than silently running a
    // different cadence — `FlushPolicy: FromStr` errors with `imbh::Error`, so `?` carries the reason
    // out through the open-error slot.
    if !w.flush.trim().is_empty() {
        b = b.flush(w.flush.parse::<imbh::FlushPolicy>()?);
    }
    // imbh 0.5.0's duplicate-timestamp policy. Unlike `flush`, unset and an explicit default are the
    // same thing here (`Duplicates::ErrorOnRead`, what the builder already holds), so the guard only
    // avoids a pointless parse. A malformed spec still fails the open rather than silently running a
    // policy the caller did not ask for — `Duplicates: FromStr` errors with `imbh::Error`, so `?`
    // carries the reason out through the open-error slot.
    if !w.duplicates.trim().is_empty() {
        b = b.duplicates(w.duplicates.parse::<imbh::Duplicates>()?);
    }
    if !w.promote_keys.is_empty() {
        b = b.promote(imbh::Promote::new(w.promote_keys.clone()));
    }
    b.open()
}

/// Close (drop) a Db handle. Unknown ids are ignored.
#[unsafe(no_mangle)]
pub extern "C" fn imbhgo_close(id: u64) {
    let _ = DBS.lock().unwrap().remove(&id);
}
