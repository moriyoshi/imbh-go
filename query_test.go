package imbhgo

import (
	"context"
	"testing"
	"time"
)

func countRows(t *testing.T, rows *Rows) int {
	t.Helper()
	total := 0
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		total += int(rec.NumRows())
		rec.Release()
	}
	if rows.Err() != nil {
		t.Fatalf("Err: %v", rows.Err())
	}
	return total
}

// TestQueryLogsTyped exercises the typed log query (native struct → IMBH LogQuery builder → zero-copy
// rows) across service, full-text, and limit filters.
func TestQueryLogsTyped(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", []string{"error happened", "all ok"})); err != nil {
		t.Fatalf("ingest checkout: %v", err)
	}
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "api", []string{"hello world"})); err != nil {
		t.Fatalf("ingest api: %v", err)
	}

	cases := []struct {
		name string
		q    LogQuery
		want int
	}{
		{"all", LogQuery{}, 3},
		{"service", LogQuery{Service: "checkout"}, 2},
		{"service-api", LogQuery{Service: "api"}, 1},
		{"fulltext", LogQuery{Match: "error"}, 1},
		{"limit", LogQuery{Limit: 1}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows, err := db.QueryLogs(context.Background(), c.q)
			if err != nil {
				t.Fatalf("QueryLogs: %v", err)
			}
			defer rows.Close()
			if got := countRows(t, rows); got != c.want {
				t.Fatalf("rows = %d, want %d", got, c.want)
			}
		})
	}
}

// TestCountLogs exercises CountLogs (imbh's logs().count over a typed LogQuery): filtered totals, and
// that Limit is ignored by the count (it bounds returned rows, not the tally).
func TestCountLogs(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", []string{"error happened", "all ok"})); err != nil {
		t.Fatalf("ingest checkout: %v", err)
	}
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "api", []string{"hello world"})); err != nil {
		t.Fatalf("ingest api: %v", err)
	}

	cases := []struct {
		name string
		q    LogQuery
		want uint64
	}{
		{"all", LogQuery{}, 3},
		{"service", LogQuery{Service: "checkout"}, 2},
		{"service-api", LogQuery{Service: "api"}, 1},
		{"fulltext", LogQuery{Match: "error"}, 1},
		{"limit-ignored", LogQuery{Limit: 1}, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := db.CountLogs(context.Background(), c.q)
			if err != nil {
				t.Fatalf("CountLogs: %v", err)
			}
			if got != c.want {
				t.Fatalf("CountLogs = %d, want %d", got, c.want)
			}
		})
	}
}

// TestQueryMetricsTyped smoke-tests the typed metric range query against an empty metrics table: it
// must return a clean, empty result (not an error).
func TestQueryMetricsTyped(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	now := time.Now().UnixNano()
	rows, err := db.QueryMetrics(context.Background(), MetricQuery{
		Metric: "cpu.utilization",
		Start:  now - int64(time.Hour),
		End:    now,
		Step:   int64(time.Second),
	})
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	defer rows.Close()
	if got := countRows(t, rows); got != 0 {
		t.Fatalf("rows = %d, want 0 (no metrics ingested)", got)
	}
}
