// Package protocdata is an isolated prototype (binding plan §2, risk #1): import a Rust-produced
// FfiBatch via arrow-go/cdata and exercise the two-free ownership protocol. No sable, no imbh — just
// the Arrow C Data Interface. cgo must live in a non-test file (Go forbids `import "C"` in _test.go).
package protocdata

/*
#cgo LDFLAGS: -L${SRCDIR}/rust/target/release -lprotobatch -lpthread -lm -ldl

#include <stdint.h>

// The Arrow C Data Interface ABI structs (must match arrow-rs FFI_ArrowArray / FFI_ArrowSchema and
// arrow-go/cdata's own C structs — all three are the stable C Data Interface layout).
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
// Matches Rust's #[repr(C)] FfiBatch { array, schema }.
struct FfiBatch {
    struct ArrowArray array;
    struct ArrowSchema schema;
};

uint64_t imbhgo_test_batch(void);
void     imbhgo_shell_free(uint64_t ptr);
void     imbhgo_batch_release(uint64_t ptr);

// Whether the array's release callback is still armed (non-null) — to learn arrow-go's move semantics.
static int array_release_is_armed(uint64_t ptr) {
    struct FfiBatch* b = (struct FfiBatch*)(uintptr_t)ptr;
    return b->array.release != 0;
}
*/
import "C"

import (
	"unsafe"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/cdata"
)

// testBatch produces one Rust FfiBatch and returns its pointer (as sable's Payload::Handle carries it).
func testBatch() uint64 { return uint64(C.imbhgo_test_batch()) }

// importBatch imports a Rust FfiBatch zero-copy and frees the shell (the taken path): the returned
// RecordBatch owns the buffers; its Release() drives the Rust release callback.
func importBatch(ptr uint64) (arrow.RecordBatch, error) {
	fb := (*C.struct_FfiBatch)(unsafe.Pointer(uintptr(ptr)))
	rec, err := cdata.ImportCRecordBatch(
		(*cdata.CArrowArray)(unsafe.Pointer(&fb.array)),
		(*cdata.CArrowSchema)(unsafe.Pointer(&fb.schema)),
	)
	if err != nil {
		return nil, err
	}
	C.imbhgo_shell_free(C.uint64_t(ptr)) // arrays now owned by rec; free only the shell
	return rec, nil
}

// importOnly imports a batch WITHOUT freeing the shell, and reports whether the source array's
// release is still armed afterward — the definitive move-semantics probe. Caller must shell_free.
func importOnly(ptr uint64) (rec arrow.RecordBatch, armedAfter bool, err error) {
	fb := (*C.struct_FfiBatch)(unsafe.Pointer(uintptr(ptr)))
	rec, err = cdata.ImportCRecordBatch(
		(*cdata.CArrowArray)(unsafe.Pointer(&fb.array)),
		(*cdata.CArrowSchema)(unsafe.Pointer(&fb.schema)),
	)
	if err != nil {
		return nil, false, err
	}
	return rec, arrayReleaseArmed(ptr), nil
}

// shellFree frees only the shell allocation (forget inner) — the taken path.
func shellFree(ptr uint64) { C.imbhgo_shell_free(C.uint64_t(ptr)) }

// batchRelease fully frees a never-imported batch (the abandoned path — as sable's cursor drain does).
func batchRelease(ptr uint64) { C.imbhgo_batch_release(C.uint64_t(ptr)) }

// arrayReleaseArmed reports whether the batch's array release callback is still set.
func arrayReleaseArmed(ptr uint64) bool { return C.array_release_is_armed(C.uint64_t(ptr)) != 0 }
