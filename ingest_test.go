package imbhgo

import (
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// makeLogsRequest builds an OTLP ExportLogsServiceRequest (one resource, one scope, N log records)
// and marshals it — the exact bytes a stock OTLP/HTTP exporter would POST.
func makeLogsRequest(t *testing.T, service string, bodies []string) []byte {
	t.Helper()
	records := make([]*logspb.LogRecord, len(bodies))
	for i, body := range bodies {
		records[i] = &logspb.LogRecord{
			TimeUnixNano:   uint64(1_700_000_000_000_000_000 + i),
			SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
			SeverityText:   "INFO",
			Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
		}
	}
	req := &collogspb.ExportLogsServiceRequest{
		ResourceLogs: []*logspb.ResourceLogs{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{{
					Key:   "service.name",
					Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}},
				}},
			},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
		}},
	}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal OTLP: %v", err)
	}
	return b
}

// countLogs runs `SELECT count(*) FROM logs` and returns the count (summing all batch rows, though
// count(*) yields a single row).
func countLogs(t *testing.T, db *DB) int64 {
	t.Helper()
	rows, err := db.Query(context.Background(), "SELECT count(*) AS n FROM logs")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	var n int64
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
			n += col.Value(i)
		}
		rec.Release()
	}
	return n
}

// TestIngestAndQuery is the M2 loop: ingest OTLP logs, query the buffer, flush to a segment, query the
// segment (which drives IMBH's lazy per-batch scan through the zero-copy path).
func TestIngestAndQuery(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	otlp := makeLogsRequest(t, "checkout", []string{"hello", "world", "third log line"})
	r, err := db.IngestOTLPLogs(otlp)
	if err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}
	if r.Accepted != 3 {
		t.Fatalf("Accepted = %d, want 3 (rejected=%d)", r.Accepted, r.Rejected)
	}

	// Queryable immediately (buffer path).
	if n := countLogs(t, db); n != 3 {
		t.Fatalf("buffer count = %d, want 3", n)
	}

	// Seal to a segment, then query again (on-disk segment path → the lazy scan).
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if n := countLogs(t, db); n != 3 {
		t.Fatalf("post-flush count = %d, want 3", n)
	}
}

// TestQueryIngestedBodies pulls the actual ingested column values back through the zero-copy path.
func TestQueryIngestedBodies(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "api", []string{"aaa", "bbb"})); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	rows, err := db.Query(context.Background(), "SELECT count(*) AS n FROM logs WHERE service = 'api'")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()
	var n int64
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		n += rec.Column(0).(*array.Int64).Value(0)
		rec.Release()
	}
	if n != 2 {
		t.Fatalf("count where service='api' = %d, want 2", n)
	}
}

// TestIngestBadBytes: malformed OTLP surfaces as a Go error (byte-Call error path), not a panic.
func TestIngestBadBytes(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.IngestOTLPLogs([]byte{0xff, 0xff, 0xff, 0xff}); err == nil {
		t.Fatal("expected an error ingesting malformed OTLP bytes, got nil")
	}
}
