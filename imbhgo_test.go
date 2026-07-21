package imbhgo

import (
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
)

// TestOpenAndConstantQuery is the M0 walking skeleton: open an in-memory Db, run a constant SELECT,
// and pull the result as a zero-copy Arrow batch through the full sable→imbh→Arrow-C-Data path.
func TestOpenAndConstantQuery(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(context.Background(), "SELECT 42 AS answer")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	total := 0
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		if rec.NumCols() != 1 {
			t.Fatalf("NumCols = %d, want 1", rec.NumCols())
		}
		col, ok := rec.Column(0).(*array.Int64)
		if !ok {
			t.Fatalf("col0 type = %T, want *array.Int64", rec.Column(0))
		}
		for i := 0; i < int(rec.NumRows()); i++ {
			if col.Value(i) != 42 {
				t.Errorf("row %d = %d, want 42", i, col.Value(i))
			}
			total++
		}
		rec.Release()
	}
	if total != 1 {
		t.Fatalf("total rows = %d, want 1", total)
	}
}

// TestMultiRowQuery pulls several rows (a VALUES list) to exercise real batch content end-to-end.
func TestMultiRowQuery(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	rows, err := db.Query(context.Background(), "SELECT column1 AS x FROM (VALUES (10),(20),(30)) AS t")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	var sum int64
	n := 0
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		col := rec.Column(0).(*array.Int64)
		for i := 0; i < int(rec.NumRows()); i++ {
			sum += col.Value(i)
			n++
		}
		rec.Release()
	}
	if n != 3 || sum != 60 {
		t.Fatalf("got n=%d sum=%d, want n=3 sum=60", n, sum)
	}
}

// TestEarlyClose opens a query and closes it without draining — the cancel path (sable aborts the
// producer, imbh drops its snapshot, buffered batches are released).
func TestEarlyClose(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	for i := 0; i < 50; i++ {
		rows, err := db.Query(context.Background(), "SELECT column1 AS x FROM (VALUES (1),(2),(3),(4),(5)) AS t")
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
		rec, ok, err := rows.Next() // pull one, then abandon the rest
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if ok {
			rec.Release()
		}
		rows.Close()
	}
}
