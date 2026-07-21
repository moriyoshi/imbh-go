package imbhgo

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"
)

func fdCount(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skipf("cannot read /proc/self/fd: %v", err)
	}
	return len(ents)
}

// eventually polls cond up to d, returning true as soon as it holds (async frees/goroutine exits may
// lag the Go call that triggered them).
func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// TestAbandonedBatchesFreed deterministically exercises the abandoned path: ingest enough rows for
// several batches (IMBH's batch size is ~4096), pull one, then Close — leaving buffered batches that
// sable's cursor drain must free via imbhgo_batch_release. The live-batch counter must return to its
// starting value.
func TestAbandonedBatchesFreed(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	start := liveBatches()
	bodies := make([]string, 10000) // > 2 × batch_size → multiple result batches
	for i := range bodies {
		bodies[i] = "log line for the abandoned-batch leak test"
	}
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "svc", bodies)); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	rows, err := db.Query(context.Background(), "SELECT * FROM logs")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	rec, ok, err := rows.Next() // pull exactly one; leave the rest buffered/unproduced
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ok {
		rec.Release()
	}
	rows.Close() // abandon the remaining batches → batch_release must free them

	if !eventually(2*time.Second, func() bool { return liveBatches() == start }) {
		t.Fatalf("live batches = %d, want %d (abandoned batch leaked)", liveBatches(), start)
	}
}

// TestNoLeak is the ownership/leak gate (binding plan hardening): many mixed ingest/query/close cycles
// must leave the live-batch counter at 0 (every export freed exactly once via shell_free or
// batch_release — no leak, no double free), drain the query-error and Db registries, and return
// goroutines/fds to baseline.
func TestNoLeak(t *testing.T) {
	// Warm up sable's persistent goroutines/fds before sampling the baseline.
	warm, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	rows, _ := warm.Query(context.Background(), "SELECT 1 AS x")
	for {
		rec, ok, _ := rows.Next()
		if !ok {
			break
		}
		rec.Release()
	}
	rows.Close()
	warm.Close()
	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	startBatches := liveBatches()
	baseGoroutine := runtime.NumGoroutine()
	baseFD := fdCount(t)

	const N = 150
	for i := 0; i < N; i++ {
		db, err := OpenInMemory()
		if err != nil {
			t.Fatalf("iter %d OpenInMemory: %v", i, err)
		}
		if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "svc", []string{"a", "b", "c", "d", "e"})); err != nil {
			t.Fatalf("iter %d ingest: %v", i, err)
		}

		// (a) full drain — the taken path (shell_free) for every batch.
		r1, _ := db.Query(context.Background(), "SELECT * FROM logs")
		for {
			rec, ok, err := r1.Next()
			if err != nil {
				t.Fatalf("iter %d drain: %v", i, err)
			}
			if !ok {
				break
			}
			rec.Release()
		}
		r1.Close()

		// (b) close without draining — the abandoned path (batch_release) for any buffered batch.
		r2, _ := db.Query(context.Background(), "SELECT * FROM logs")
		r2.Close()

		// (c) a failing query — exercises the query-error registry store/fetch cycle.
		r3, _ := db.Query(context.Background(), "SELECT * FROM no_such_table")
		if _, ok, _ := r3.Next(); ok {
			t.Fatalf("iter %d: bad query returned a row", i)
		}
		r3.Close()

		db.Close()
	}
	runtime.GC()

	// Ownership: the live-batch counter must settle back to its starting value (async frees may lag).
	if !eventually(2*time.Second, func() bool { return liveBatches() == startBatches }) {
		t.Fatalf("live batches = %d, want %d (leaked shell or double free)", liveBatches(), startBatches)
	}
	if pe := pendingQueryErrors(); pe != 0 {
		t.Errorf("pending query errors = %d, want 0 (error slot leak)", pe)
	}
	if d := liveDBs(); d != 0 {
		t.Errorf("live dbs = %d, want 0 (Db handle leak)", d)
	}

	// Goroutines and fds return to ~baseline (small slack for runtime jitter).
	if !eventually(2*time.Second, func() bool { return runtime.NumGoroutine() <= baseGoroutine+4 }) {
		t.Errorf("goroutines = %d, base %d (goroutine leak)", runtime.NumGoroutine(), baseGoroutine)
	}
	if fc := fdCount(t); fc > baseFD+8 {
		t.Errorf("fds = %d, base %d (fd leak)", fc, baseFD)
	}
}
