//go:build sable_extern_lib

package imbhgo

import (
	"context"
	"testing"
	"time"
)

// Abandoning a stream must not strand its terminal error on the Rust side. A query that fails at
// plan time records its error the moment the handler runs, before Go pulls anything; if the caller
// then closes without draining to end-of-stream, finish never runs, and nothing else would ever
// claim that slot. It used to be held until the process exited — an unbounded leak for a long-lived
// program that abandons streams (a cancelled request handler, an early return under `defer Close`).
//
// The wait before Close is what makes this deterministic rather than a race with the handler: it
// pins the ordering to "stored, then abandoned", which is the case Close is responsible for.
func TestAbandonedStreamClearsErrorSlot(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	start := pendingQueryErrors()

	rows, err := db.Query(context.Background(), "SELECT * FROM no_such_table")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if !eventually(2*time.Second, func() bool { return pendingQueryErrors() == start+1 }) {
		t.Fatalf("plan error never stored: pending = %d, want %d", pendingQueryErrors(), start+1)
	}

	rows.Close() // abandoned: no Next, so no end-of-stream and no finish

	if !eventually(2*time.Second, func() bool { return pendingQueryErrors() == start }) {
		t.Errorf("pending query errors = %d, want %d (error slot leaked by an abandoned stream)",
			pendingQueryErrors(), start)
	}
}

// Closing a drained stream must not double-fetch or otherwise disturb the reported error: finish
// already claimed the slot and marked the stream ended, so Close has nothing left to do.
func TestDrainedStreamKeepsItsError(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(context.Background(), "SELECT * FROM no_such_table")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	for {
		rec, ok, err := rows.Next()
		if !ok {
			if err == nil {
				t.Fatal("drained a failing query with no error")
			}
			break
		}
		rec.Release()
	}
	want := rows.Err()
	if want == nil {
		t.Fatal("Err() is nil after a failing query")
	}
	rows.Close()
	rows.Close() // idempotent
	if got := rows.Err(); got != want {
		t.Errorf("Err() changed across Close: %v -> %v", want, got)
	}
}
