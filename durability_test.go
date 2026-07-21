package imbhgo

import (
	"context"
	"testing"
	"time"
)

// TestDurabilityReopen is the on-disk persistence gate: ingest a known dataset into an on-disk
// database, Flush() to seal it, Close(), then re-Open() the SAME path and assert the data is still
// there WITHOUT re-ingesting. This exercises the WAL-replay / segment-reload path across a fresh
// process-local Db instance. The test is hermetic: it uses t.TempDir() only, no network, no daemons.
func TestDurabilityReopen(t *testing.T) {
	dir := t.TempDir()
	base := int64(1_700_000_000_000_000_000)

	const nLogs = 5
	logBodies := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	if len(logBodies) != nLogs {
		t.Fatalf("test setup: %d bodies, want %d", len(logBodies), nLogs)
	}
	gaugeValues := []float64{10, 20, 30, 40} // sum = 100
	var wantGaugeSum float64
	for _, v := range gaugeValues {
		wantGaugeSum += v
	}

	// --- Phase 1: fresh on-disk Db, ingest, seal, verify, close. --------------------------------
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}

	r, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", logBodies))
	if err != nil {
		db.Close()
		t.Fatalf("IngestOTLPLogs: %v", err)
	}
	if r.Accepted != nLogs {
		db.Close()
		t.Fatalf("Accepted = %d, want %d (rejected=%d)", r.Accepted, nLogs, r.Rejected)
	}
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "checkout", "cpu.util", gaugeValues, base)); err != nil {
		db.Close()
		t.Fatalf("IngestOTLPMetrics: %v", err)
	}

	// Seal the in-memory buffer into an on-disk segment to make it durable.
	if err := db.Flush(); err != nil {
		db.Close()
		t.Fatalf("Flush: %v", err)
	}

	// Verify the data is present before closing (still same process, on-disk path after Flush).
	if n := countLogs(t, db); n != nLogs {
		db.Close()
		t.Fatalf("pre-close log count = %d, want %d", n, nLogs)
	}
	if got := sumGauge(t, db, base); got != wantGaugeSum {
		db.Close()
		t.Fatalf("pre-close gauge sum = %v, want %v", got, wantGaugeSum)
	}
	db.Close()

	// --- Phase 2: re-open the SAME path, query WITHOUT re-ingesting. -----------------------------
	db2, err := Open(dir)
	if err != nil {
		t.Fatalf("re-Open(%q): %v", dir, err)
	}
	defer db2.Close()

	if n := countLogs(t, db2); n != nLogs {
		t.Fatalf("post-reopen log count = %d, want %d (data did not persist across reopen)", n, nLogs)
	}
	if got := sumGauge(t, db2, base); got != wantGaugeSum {
		t.Fatalf("post-reopen gauge sum = %v, want %v (metric did not persist)", got, wantGaugeSum)
	}

	// Typed log query survives the reopen too, and each ingested body comes back.
	logs, err := db2.QueryLogsTyped(context.Background(), LogQuery{
		Service: "checkout",
		Start:   base - int64(time.Hour),
		End:     base + int64(time.Hour),
	})
	if err != nil {
		t.Fatalf("post-reopen QueryLogsTyped: %v", err)
	}
	if len(logs) != nLogs {
		t.Fatalf("post-reopen typed logs = %d, want %d", len(logs), nLogs)
	}
	seen := make(map[string]bool, nLogs)
	for _, l := range logs {
		seen[l.Body] = true
	}
	for _, want := range logBodies {
		if !seen[want] {
			t.Errorf("post-reopen: body %q missing from persisted logs", want)
		}
	}
}

// sumGauge sums the raw gauge points for cpu.util in a wide window around base.
func sumGauge(t *testing.T, db *DB, base int64) float64 {
	t.Helper()
	pts, err := db.QueryMetricPointsTyped(context.Background(), MetricPointsQuery{
		Metric: "cpu.util",
		Start:  base - int64(time.Hour),
		End:    base + int64(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryMetricPointsTyped: %v", err)
	}
	var sum float64
	for _, p := range pts {
		sum += p.Value
	}
	return sum
}
