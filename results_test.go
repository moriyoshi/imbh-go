package imbhgo

import (
	"context"
	"testing"
)

// TestQueryLogsTypedDecode: the Arrow→struct decoder turns log query results into []LogEntry with the
// right field values (service via Dictionary(Utf8), body via Utf8, time via Timestamp).
func TestQueryLogsTypedDecode(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", []string{"error happened", "all ok"})); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	entries, err := db.QueryLogsTyped(context.Background(), LogQuery{Service: "checkout"})
	if err != nil {
		t.Fatalf("QueryLogsTyped: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	bodies := map[string]bool{}
	for _, e := range entries {
		if e.Service != "checkout" {
			t.Errorf("Service = %q, want checkout", e.Service)
		}
		if e.SeverityText != "INFO" {
			t.Errorf("SeverityText = %q, want INFO", e.SeverityText)
		}
		if e.Time == 0 {
			t.Errorf("Time is zero, want the ingested timestamp")
		}
		bodies[e.Body] = true
	}
	if !bodies["error happened"] || !bodies["all ok"] {
		t.Errorf("decoded bodies = %v, want both ingested lines", bodies)
	}
}

// TestQueryLogsTypedError: a query error propagates out of the decoder (not a silent empty slice). A
// closed db is dropped from the registry, so the handler reports "unknown db handle".
func TestQueryLogsTypedError(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	db.Close() // drop from the registry
	if _, err := db.QueryLogsTyped(context.Background(), LogQuery{Service: "x"}); err == nil {
		t.Fatal("QueryLogsTyped on a closed db returned nil error, want a failure")
	}
}
