// Package imbhgo is a Go binding for IMBH — an embeddable observability database — with zero-copy
// Arrow query results fused onto Go's scheduler via sable. (Binding plan M0: open → SQL → zero-copy
// rows. Ingest and typed queries land in M1/M2.)
//
// Build with -tags sable_extern_lib so sable's Go package contributes no -lsable of its own; the
// combined staticlib below (which contains both imbhgo_* and sable_* symbols) is linked instead.
package imbhgo

/*
#cgo CFLAGS: -I${SRCDIR}

// The LDFLAGS that select the combined libimbhgo.a live in the GOOS-gated link_*.go files, so the
// archive search path and system libs are correct per platform. Local builds resolve the archive
// from ${SRCDIR}/rust/target/release; a downstream consumer using a prebuilt archive supplies its
// location via CGO_LDFLAGS (see cmd/imbhgo-fetch and README).

#include "imbhgo.h"
#include <stdint.h>

// The Arrow C Data Interface ABI structs (match arrow-rs FFI_ArrowArray/Schema, arrow-go/cdata, and
// Rust's #[repr(C)] FfiBatch). Validated in proto-cdata/.
struct ArrowSchema {
    const char* format;
    const char* name;
    const char* metadata;
    int64_t flags;
    int64_t n_children;
    struct ArrowSchema** children;
    struct ArrowSchema* dictionary;
    void (*release)(struct ArrowSchema*);
    void* private_data;
};
struct ArrowArray {
    int64_t length;
    int64_t null_count;
    int64_t offset;
    int64_t n_buffers;
    int64_t n_children;
    const void** buffers;
    struct ArrowArray** children;
    struct ArrowArray* dictionary;
    void (*release)(struct ArrowArray*);
    void* private_data;
};
struct FfiBatch {
    struct ArrowArray array;
    struct ArrowSchema schema;
};

// Return the field addresses from C, so Go never converts a uintptr to unsafe.Pointer (which
// -race/checkptr flags). The returned pointers are into Rust-owned (non-Go-heap) memory.
static struct ArrowArray*  ffi_batch_array(uint64_t ptr)  { return &((struct FfiBatch*)(uintptr_t)ptr)->array; }
static struct ArrowSchema* ffi_batch_schema(uint64_t ptr) { return &((struct FfiBatch*)(uintptr_t)ptr)->schema; }
*/
import "C"

import (
	"context"
	"encoding/binary"
	"errors"
	"runtime"
	"sync/atomic"
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/cdata"
	"github.com/moriyoshi/sable"
)

// Op ids shared with rust/src/lib.rs.
const (
	opSQL        uint32 = 1
	opQueryError uint32 = 6
)

// queryCtr issues a unique id per query so the Rust side can report a terminal error out-of-band
// (the S-3 stream wire has no error channel — a 0 handle only means "no batch").
var queryCtr atomic.Uint64

func init() {
	// Register handlers before sable builds its runtime, then build it.
	C.imbhgo_init()
	sable.Init()
}

// DB is a handle to an embedded IMBH database.
type DB struct{ id uint64 }

// OpenInMemory opens an ephemeral, process-local database (great for tests and dev loops).
func OpenInMemory() (*DB, error) {
	id := uint64(C.imbhgo_open_memory())
	if id == 0 {
		return nil, errors.New("imbhgo: open in-memory database failed")
	}
	return &DB{id: id}, nil
}

// Open opens a durable, on-disk database at path (created if absent).
func Open(path string) (*DB, error) {
	b := []byte(path)
	var p *C.uint8_t
	if len(b) > 0 {
		p = (*C.uint8_t)(unsafe.Pointer(&b[0]))
	}
	id := uint64(C.imbhgo_open(p, C.size_t(len(b))))
	runtime.KeepAlive(b)
	if id == 0 {
		return nil, errors.New("imbhgo: open database at " + path + " failed")
	}
	return &DB{id: id}, nil
}

// Close drops the database handle.
func (db *DB) Close() { C.imbhgo_close(C.uint64_t(db.id)) }

// Query runs a SQL statement and returns a lazily-streamed, zero-copy result set. The query executes
// batch-by-batch on IMBH's engine (fused onto Go's scheduler via sable); each Rows.Next pulls one
// Arrow RecordBatch without copying its buffers. Cancelling ctx aborts a parked Next (interrupting a
// slow batch) and cancels the query on the IMBH side (releasing its pinned snapshot); after
// cancellation, Next returns ok=false with ctx.Err().
func (db *DB) Query(ctx context.Context, sql string) (*Rows, error) {
	return db.openStream(ctx, opSQL, []byte(sql))
}

// openStream opens a result stream for a query op: it assigns a query id, frames the request as
// [8-byte db id][8-byte query id][payload], opens the sable cursor, and wraps it in Rows. Shared by
// the SQL and typed-query entry points. Returns ErrBackpressure if the runtime is at its cap.
func (db *DB) openStream(ctx context.Context, op uint32, payload []byte) (*Rows, error) {
	qid := queryCtr.Add(1)
	req := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint64(req[0:8], db.id)
	binary.LittleEndian.PutUint64(req[8:16], qid)
	copy(req[16:], payload)
	s, err := sable.OpenStream(op, req)
	if err != nil {
		return nil, err
	}
	return &Rows{s: s, db: db, queryID: qid, ctx: ctx}, nil
}

// Rows is a streaming, zero-copy query result. Iterate with Next until it returns ok=false, then
// check Err (or Close). Not safe for concurrent use: iterate from one goroutine. Each returned
// RecordBatch is owned by the caller — call its Release() when done.
//
// ZERO-COPY CAVEAT: a batch's Arrow buffers are IMBH-owned and freed by Release(). Scalar values read
// from a batch — especially strings/[]byte via arrow-go, which alias the buffer without copying — are
// only valid until that batch's Release(). Copy anything you need to outlive the batch (e.g.
// strings.Clone for strings; QueryLogsTyped does this for you).
type Rows struct {
	s       *sable.Stream
	db      *DB
	queryID uint64
	ctx     context.Context
	ended   bool
	err     error
}

// Next pulls the next result batch. ok=false marks the end of iteration; then Err reports whether it
// ended cleanly (nil), on a query error, or on context cancellation. On ok=true the RecordBatch wraps
// IMBH-allocated Arrow buffers zero-copy; the caller must Release() it.
func (r *Rows) Next() (rec arrow.RecordBatch, ok bool, err error) {
	if r.ended {
		return nil, false, r.err
	}
	ptr, ok := r.s.NextCtx(r.ctx)
	if !ok {
		return nil, false, r.finish(nil) // end-of-stream or cancellation
	}
	rec, err = cdata.ImportCRecordBatch(
		(*cdata.CArrowArray)(unsafe.Pointer(C.ffi_batch_array(C.uint64_t(ptr)))),
		(*cdata.CArrowSchema)(unsafe.Pointer(C.ffi_batch_schema(C.uint64_t(ptr)))),
	)
	C.imbhgo_shell_free(C.uint64_t(ptr)) // arrays now owned by rec; free only the shell
	if err != nil {
		return nil, false, r.finish(err)
	}
	return rec, true, nil
}

// finish records the terminal state once, closes the cursor, and returns the terminal error (nil on a
// clean end). Precedence: an import error (passed in) → context cancellation → a stored query error.
func (r *Rows) finish(importErr error) error {
	stored := r.fetchQueryError() // always fetch to clear the Rust-side slot
	switch {
	case importErr != nil:
		r.err = importErr
	case r.ctx != nil && r.ctx.Err() != nil:
		r.err = r.ctx.Err()
	default:
		r.err = stored
	}
	r.ended = true
	r.Close()
	return r.err
}

// fetchQueryError retrieves and clears this query's terminal error from the Rust side (empty = clean).
func (r *Rows) fetchQueryError() error {
	req := make([]byte, 8)
	binary.LittleEndian.PutUint64(req, r.queryID)
	resp, err := sable.Call(opQueryError, req)
	if err != nil {
		return err
	}
	if len(resp) > 0 {
		return errors.New("imbhgo: query failed: " + string(resp))
	}
	return nil
}

// Err returns the terminal error after Next has returned ok=false: nil (clean end), a query error, or
// context.Canceled/DeadlineExceeded. Meaningless before iteration ends.
func (r *Rows) Err() error { return r.err }

// Close releases the cursor (and cancels the query if not fully drained). Idempotent.
func (r *Rows) Close() { r.s.Close() }
