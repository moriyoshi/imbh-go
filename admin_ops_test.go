package imbhgo

import (
	"path/filepath"
	"testing"
)

// TestOpenReadOnly seals an on-disk dataset with the writer, closes it, then opens the same path
// read-only and reads it back. A read-only handle must reject writes.
func TestOpenReadOnly(t *testing.T) {
	dir := t.TempDir()
	bodies := []string{"alpha", "bravo", "charlie"}

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", bodies)); err != nil {
		db.Close()
		t.Fatalf("IngestOTLPLogs: %v", err)
	}
	if err := db.Flush(); err != nil {
		db.Close()
		t.Fatalf("Flush: %v", err)
	}
	db.Close()

	ro, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	if n := countLogs(t, ro); n != int64(len(bodies)) {
		t.Fatalf("read-only log count = %d, want %d", n, len(bodies))
	}
	// A write against a read-only handle must fail.
	if _, err := ro.IngestOTLPLogs(makeLogsRequest(t, "checkout", []string{"x"})); err == nil {
		t.Fatalf("IngestOTLPLogs on read-only handle: got nil error, want a read-only rejection")
	}
}

// TestOpenWith opens with a configured builder (compression + memory budget) and round-trips data,
// then re-opens the same path read-only via the options path.
func TestOpenWith(t *testing.T) {
	dir := t.TempDir()
	bodies := []string{"one", "two", "three", "four"}

	db, err := OpenWith(DbOptions{
		Path:              dir,
		Compression:       "zstd",
		ZstdLevel:         3,
		MemoryBudgetBytes: 64 << 20,
	})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", bodies)); err != nil {
		db.Close()
		t.Fatalf("IngestOTLPLogs: %v", err)
	}
	if err := db.Flush(); err != nil {
		db.Close()
		t.Fatalf("Flush: %v", err)
	}
	if n := countLogs(t, db); n != int64(len(bodies)) {
		db.Close()
		t.Fatalf("log count = %d, want %d", n, len(bodies))
	}
	db.Close()

	ro, err := OpenWith(DbOptions{Path: dir, ReadOnly: true})
	if err != nil {
		t.Fatalf("OpenWith(ReadOnly): %v", err)
	}
	defer ro.Close()
	if n := countLogs(t, ro); n != int64(len(bodies)) {
		t.Fatalf("read-only (options) log count = %d, want %d", n, len(bodies))
	}
}

// TestOpsPassthrough exercises the admin/lifecycle byte-Call surface end to end.
func TestOpsPassthrough(t *testing.T) {
	dir := t.TempDir()
	base := int64(1_700_000_000_000_000_000)
	bodies := []string{"alpha", "bravo", "charlie", "delta", "echo"}
	nLogs := int64(len(bodies))

	db, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", bodies)); err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "checkout", "cpu.util", []float64{1, 2, 3}, base)); err != nil {
		t.Fatalf("IngestOTLPMetrics: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Stats: the logs table should account for exactly the ingested rows.
	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	var logRows uint64
	var sawLogs bool
	for _, ts := range stats.Tables {
		if ts.Table == string(TableLogs) {
			sawLogs = true
			logRows = ts.SegmentRows + ts.BufferRows
		}
	}
	if !sawLogs {
		t.Fatalf("Stats: no %q table in %+v", TableLogs, stats.Tables)
	}
	if logRows != uint64(nLogs) {
		t.Fatalf("Stats: logs rows = %d, want %d", logRows, nLogs)
	}

	// Segments / SegmentFiles: after a flush there is at least one sealed logs segment on disk.
	segs, err := db.Segments()
	if err != nil {
		t.Fatalf("Segments: %v", err)
	}
	if len(segs) == 0 {
		t.Fatalf("Segments: got 0 after flush, want >= 1")
	}
	files, err := db.SegmentFiles(TableLogs)
	if err != nil {
		t.Fatalf("SegmentFiles: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("SegmentFiles(logs): got 0, want >= 1")
	}

	// DurableThrough / Maintain / Compact / Snapshot: exercise the calls (values are environment-
	// dependent; assert only that the round-trip succeeds).
	if _, _, err := db.DurableThrough(); err != nil {
		t.Fatalf("DurableThrough: %v", err)
	}
	if _, err := db.Maintain(); err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if _, err := db.Compact(); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if _, err := db.Snapshot(filepath.Join(t.TempDir(), "snap")); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Export / ExportRecords: the logs table decodes back to exactly the ingested rows.
	recs, err := db.ExportRecords(TableLogs, 0, 0)
	if err != nil {
		t.Fatalf("ExportRecords: %v", err)
	}
	var exported int64
	for _, rec := range recs {
		exported += rec.NumRows()
		rec.Release()
	}
	if exported != nLogs {
		t.Fatalf("ExportRecords(logs): %d rows, want %d", exported, nLogs)
	}

	// Unknown table names are rejected, not silently empty.
	if _, err := db.SegmentFiles(Table("nope")); err == nil {
		t.Fatalf("SegmentFiles(nope): got nil error, want unknown-table rejection")
	}
}
