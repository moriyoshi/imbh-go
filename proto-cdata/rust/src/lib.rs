//! Isolated risk-reduction prototype (binding plan §2, risk #1): validate the zero-copy batch
//! handoff — Rust `RecordBatch` → `FfiBatch{FFI_ArrowArray, FFI_ArrowSchema}` → Go `arrow-go/cdata`
//! import → the two-free ownership protocol — with NO sable/imbh in the picture. If this round-trips
//! cleanly under `-race`, the §2 protocol is sound and the M0 walking skeleton can build on it.

use std::sync::Arc;

use arrow::array::{Array, ArrayRef, Int64Array, RecordBatch, StringArray, StructArray};
use arrow::datatypes::{DataType, Field, Schema};
use arrow::ffi::{FFI_ArrowArray, FFI_ArrowSchema, to_ffi};

/// One batch handed to Go: array + schema behind a single pointer. `#[repr(C)]` so Go can take the
/// addresses of the two fields at fixed offsets (array first, then schema).
#[repr(C)]
pub struct FfiBatch {
    array: FFI_ArrowArray,
    schema: FFI_ArrowSchema,
}

/// A fixed 3-row, 2-column batch (`id: Int64`, `name: Utf8`) — Utf8 not Utf8View, matching how imbh
/// reads Parquet. The Go side asserts these exact values.
fn make_batch() -> RecordBatch {
    let ids = Int64Array::from(vec![10i64, 20, 30]);
    let names = StringArray::from(vec!["a", "bb", "ccc"]);
    let schema = Arc::new(Schema::new(vec![
        Field::new("id", DataType::Int64, false),
        Field::new("name", DataType::Utf8, false),
    ]));
    RecordBatch::try_new(
        schema,
        vec![Arc::new(ids) as ArrayRef, Arc::new(names) as ArrayRef],
    )
    .expect("build batch")
}

/// Produce one `FfiBatch` and hand its pointer to Go (as `u64`, exactly how sable's `Payload::Handle`
/// carries it). Ownership: the returned pointer's arrow buffers are live until either
/// `imbhgo_shell_free` (taken path) or `imbhgo_batch_release` (abandoned path) runs.
#[unsafe(no_mangle)]
pub extern "C" fn imbhgo_test_batch() -> u64 {
    let sa = StructArray::from(make_batch());
    let (array, schema) = to_ffi(&sa.into_data()).expect("to_ffi");
    Box::into_raw(Box::new(FfiBatch { array, schema })) as u64
}

/// Taken path: Go has imported the batch (its `RecordBatch` now owns the buffers' release). Free ONLY
/// the shell allocation; `forget` the inner FFI structs so their `Drop` does NOT also release the
/// buffers. Correct whether or not arrow-go nulls the source struct on import.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn imbhgo_shell_free(ptr: u64) {
    if ptr != 0 {
        std::mem::forget(*unsafe { Box::from_raw(ptr as *mut FfiBatch) });
    }
}

/// Abandoned path: the batch was never imported (e.g. buffered when a cursor was closed). FULL drop —
/// the FFI structs' own `Drop` releases the still-live arrow buffers, then the shell is freed.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn imbhgo_batch_release(ptr: u64) {
    if ptr != 0 {
        drop(unsafe { Box::from_raw(ptr as *mut FfiBatch) });
    }
}
