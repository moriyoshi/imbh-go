package imbhgo

import (
	"context"
	"errors"
	"testing"
)

// TestQueryErrorSurfaced: a query that fails on the IMBH side (unknown table) surfaces as a real Go
// error via the out-of-band query-error channel — not a silent empty result.
func TestQueryErrorSurfaced(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(context.Background(), "SELECT * FROM no_such_table")
	if err != nil {
		t.Fatalf("Query returned early error: %v", err)
	}
	defer rows.Close()

	rec, ok, nerr := rows.Next()
	if ok {
		rec.Release()
		t.Fatal("expected no rows for a failing query")
	}
	if nerr == nil {
		t.Fatal("Next returned nil error for a failing query")
	}
	if rows.Err() == nil {
		t.Fatal("Err() should report the failure")
	}
	t.Logf("surfaced query error: %v", nerr)
}

// TestQueryCleanEnd: a successful query reports Err()==nil after iteration (no false positives from
// the error channel).
func TestQueryCleanEnd(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(context.Background(), "SELECT 1 AS x")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			break
		}
		rec.Release()
	}
	if rows.Err() != nil {
		t.Fatalf("Err() = %v, want nil", rows.Err())
	}
}

// TestQueryContextCancel: cancelling the context terminates iteration and Err() reports
// context.Canceled.
func TestQueryContextCancel(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())
	rows, err := db.Query(ctx, "SELECT column1 FROM (VALUES (1),(2),(3),(4),(5)) AS t")
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer rows.Close()

	cancel() // cancel before draining
	for {
		rec, ok, _ := rows.Next()
		if !ok {
			break
		}
		rec.Release()
	}
	if !errors.Is(rows.Err(), context.Canceled) {
		t.Fatalf("Err() = %v, want context.Canceled", rows.Err())
	}
}
