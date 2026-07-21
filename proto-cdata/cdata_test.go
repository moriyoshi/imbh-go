package protocdata

import (
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
)

func checkValues(t *testing.T, rec arrow.RecordBatch) {
	t.Helper()
	if got := rec.NumRows(); got != 3 {
		t.Fatalf("NumRows = %d, want 3", got)
	}
	if got := rec.NumCols(); got != 2 {
		t.Fatalf("NumCols = %d, want 2", got)
	}
	ids, ok := rec.Column(0).(*array.Int64)
	if !ok {
		t.Fatalf("col0 type = %T, want *array.Int64", rec.Column(0))
	}
	names, ok := rec.Column(1).(*array.String)
	if !ok {
		t.Fatalf("col1 type = %T, want *array.String", rec.Column(1))
	}
	wantID := []int64{10, 20, 30}
	wantName := []string{"a", "bb", "ccc"}
	for i := 0; i < 3; i++ {
		if ids.Value(i) != wantID[i] {
			t.Errorf("id[%d] = %d, want %d", i, ids.Value(i), wantID[i])
		}
		if names.Value(i) != wantName[i] {
			t.Errorf("name[%d] = %q, want %q", i, names.Value(i), wantName[i])
		}
	}
	if rec.Schema().Field(0).Name != "id" || rec.Schema().Field(1).Name != "name" {
		t.Errorf("schema fields = %v", rec.Schema())
	}
}

// TestImportRoundTrip: the taken path — produce, import zero-copy, verify values, free shell, release.
func TestImportRoundTrip(t *testing.T) {
	ptr := testBatch()
	if ptr == 0 {
		t.Fatal("test_batch returned null")
	}
	rec, err := importBatch(ptr)
	if err != nil {
		t.Fatalf("importBatch: %v", err)
	}
	checkValues(t, rec)
	rec.Release() // drives the Rust release callback → frees the Rust-side arrow buffers
}

// TestMoveSemantics: definitively learn whether arrow-go nulls the source array's release on import.
// This decides whether the forget-based shell free (§2) is load-bearing: if the source stays ARMED
// after import, a naive full-drop of the shell would double-release — so `forget` is essential.
func TestMoveSemantics(t *testing.T) {
	ptr := testBatch()
	armedBefore := arrayReleaseArmed(ptr)
	rec, armedAfter, err := importOnly(ptr)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	t.Logf("source array.release armed: before=%v, after import=%v", armedBefore, armedAfter)
	if armedAfter {
		t.Log("=> arrow-go does NOT null the source; the forget-based shellFree (§2) is REQUIRED " +
			"(a full drop after import would double-free the buffers).")
	} else {
		t.Log("=> arrow-go nulls the source on import; shellFree could be a plain drop, but forget " +
			"is still correct and keeps the two paths uniform.")
	}
	shellFree(ptr) // forget inner + free shell — safe regardless of armedAfter
	checkValues(t, rec)
	rec.Release()
}

// TestAbandonedRelease: the abandoned path — a batch Go never imports is fully freed by
// imbhgo_batch_release (as sable's cursor drain would do on early Close). No crash / no leak.
func TestAbandonedRelease(t *testing.T) {
	for i := 0; i < 1000; i++ {
		ptr := testBatch()
		if ptr == 0 {
			t.Fatal("null batch")
		}
		batchRelease(ptr) // full drop: releases buffers + shell
	}
}

// TestLoopTakenPath: many taken-path cycles — the leak/UAF gate under `-race`.
func TestLoopTakenPath(t *testing.T) {
	for i := 0; i < 2000; i++ {
		rec, err := importBatch(testBatch())
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if rec.NumRows() != 3 {
			t.Fatalf("iter %d: rows=%d", i, rec.NumRows())
		}
		rec.Release()
	}
}
