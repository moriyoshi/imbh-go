package imbhgo

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestTryIngest: with no cap set, the backpressure-aware ingest behaves like the blocking one.
func TestTryIngest(t *testing.T) {
	SetMaxInFlight(0) // unbounded (default)
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	r, err := db.TryIngestOTLPLogs(makeLogsRequest(t, "svc", []string{"x", "y"}))
	if err != nil {
		t.Fatalf("TryIngestOTLPLogs: %v", err)
	}
	if r.Accepted != 2 {
		t.Fatalf("Accepted = %d, want 2", r.Accepted)
	}
}

// TestBackpressureRejectsAtCap: with a cap of 2, two live result streams hold both admission slots, so
// a third Query and a TryIngest are refused with ErrBackpressure; freeing a slot re-admits. Streams
// hold their slot until Close (sable_stream_open try_admits; sable_stream_close releases), which makes
// this deterministic. The cap is global, so it is reset on exit.
func TestBackpressureRejectsAtCap(t *testing.T) {
	SetMaxInFlight(2)
	defer SetMaxInFlight(0)

	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "svc", []string{"a"})); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	// Two live streams occupy the cap.
	r1, e1 := db.Query(context.Background(), "SELECT * FROM logs")
	if e1 != nil {
		t.Fatalf("r1: %v", e1)
	}
	r2, e2 := db.Query(context.Background(), "SELECT * FROM logs")
	if e2 != nil {
		r1.Close()
		t.Fatalf("r2: %v", e2)
	}

	// A third stream is refused at the cap.
	if r3, e3 := db.Query(context.Background(), "SELECT * FROM logs"); !errors.Is(e3, ErrBackpressure) {
		if r3 != nil {
			r3.Close()
		}
		r1.Close()
		r2.Close()
		t.Fatalf("3rd query err = %v, want ErrBackpressure", e3)
	}

	// Ingest backpressure: TryIngest is refused at the cap too (the requested feature).
	if _, e := db.TryIngestOTLPLogs(makeLogsRequest(t, "svc", []string{"z"})); !errors.Is(e, ErrBackpressure) {
		r1.Close()
		r2.Close()
		t.Fatalf("TryIngest at cap err = %v, want ErrBackpressure", e)
	}
	if RuntimeStats().Rejected == 0 {
		t.Errorf("RuntimeStats().Rejected = 0, want > 0 after refusals")
	}

	// Free a slot; a new query is admitted.
	r1.Close()
	var r4 *Rows
	if !eventually(2*time.Second, func() bool {
		var e error
		r4, e = db.Query(context.Background(), "SELECT * FROM logs")
		return e == nil
	}) {
		r2.Close()
		t.Fatal("query still refused after freeing a slot")
	}
	r4.Close()
	r2.Close()
}
