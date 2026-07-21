package imbhgo

import (
	"context"
	"testing"
	"time"
)

// TestPromQLViaArrow evaluates a PromQL query and gets labeled series back through the zero-copy Arrow
// path (proving LGTM results ride the same transport as SQL/typed queries).
func TestPromQLViaArrow(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	// PromQL identifiers can't contain dots, so use an underscore metric name.
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "web", "cpu_seconds", []float64{0.2, 0.4, 0.6}, base)); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	series, err := db.QueryPromQLSeries(context.Background(), "cpu_seconds", base, base+int64(3*time.Second), int64(time.Second))
	if err != nil {
		t.Fatalf("QueryPromQLSeries: %v", err)
	}
	if len(series) == 0 {
		t.Fatal("no series returned")
	}
	total := 0
	for _, s := range series {
		total += len(s.Points)
	}
	if total == 0 {
		t.Fatal("series carried no samples")
	}
	t.Logf("promql cpu_seconds → %d series, %d samples; first labels=%v", len(series), total, series[0].Labels)
}

// TestPromQLParseError: an invalid PromQL string surfaces a parse error, not a silent empty result.
func TestPromQLParseError(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	if _, err := db.QueryPromQLSeries(context.Background(), "this is not )(valid promql", 0, 1, 1); err == nil {
		t.Fatal("expected a PromQL parse error, got nil")
	}
}

// TestLogQLViaArrow evaluates a LogQL range aggregation into labeled series.
func TestLogQLViaArrow(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", []string{"a", "b", "c"})); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	series, err := db.QueryLogQLSeries(context.Background(), `count_over_time({service="checkout"}[1h])`,
		base-int64(time.Hour), base+int64(time.Hour), int64(time.Hour))
	if err != nil {
		t.Fatalf("QueryLogQLSeries: %v", err)
	}
	total := 0.0
	for _, s := range series {
		for _, p := range s.Points {
			total += p.V
		}
	}
	t.Logf("logql count_over_time → %d series, total=%v", len(series), total)
	if len(series) == 0 {
		t.Fatal("no series returned")
	}
}

// TestTraceQLViaArrow evaluates a TraceQL query into trace/span matches.
func TestTraceQLViaArrow(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPTraces(makeTraceRequest(t, "checkout",
		[]string{"GET /a", "GET /b"}, []bool{false, true}, base)); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	// service.name is a resource attribute, so it is referenced resource-scoped in TraceQL.
	matches, err := db.QueryTraceQLMatches(context.Background(), `{ resource.service.name = "checkout" }`,
		base-int64(time.Hour), base+int64(time.Hour))
	if err != nil {
		t.Fatalf("QueryTraceQLMatches: %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("got %d trace matches, want 2", len(matches))
	}
	t.Logf("traceql → %d trace matches (first=%s)", len(matches), matches[0].TraceID)
}

// TestTraceQLThenGetSpans is the natural pairing: TraceQL yields matching trace ids, GetTraceSpans
// fetches that trace's spans as zero-copy Arrow (imbh's new traces().get_batches).
func TestTraceQLThenGetSpans(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPTraces(makeTraceRequest(t, "checkout",
		[]string{"GET /a", "GET /b"}, []bool{false, true}, base)); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	matches, err := db.QueryTraceQLMatches(context.Background(), `{ resource.service.name = "checkout" }`,
		base-int64(time.Hour), base+int64(time.Hour))
	if err != nil {
		t.Fatalf("QueryTraceQLMatches: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no traceql matches to follow up on")
	}

	// Feed a match's trace id straight into GetTraceSpans.
	spans, err := db.GetTraceSpans(context.Background(), matches[0].TraceID)
	if err != nil {
		t.Fatalf("GetTraceSpans: %v", err)
	}
	if len(spans) == 0 {
		t.Fatalf("trace %s returned no spans", matches[0].TraceID)
	}
	s := spans[0]
	if s.Service != "checkout" {
		t.Errorf("Service = %q, want checkout", s.Service)
	}
	if s.Name == "" || s.StartTime == 0 {
		t.Errorf("span missing Name/StartTime: %+v", s)
	}
	t.Logf("trace %s → %d span(s); first: name=%q service=%q status=%q dur=%dns",
		matches[0].TraceID, len(spans), s.Name, s.Service, s.StatusCode, s.DurationNs)
}

// TestLogQLSelectorLines: a bare LogQL selector returns log LINES (Loki's `streams` shape), not a
// series — the case the binding previously rejected.
func TestLogQLSelectorLines(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout",
		[]string{"payment error: timeout", "all good"})); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "api", []string{"served /health"})); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	lines, err := db.QueryLogQLLines(context.Background(), `{service="checkout"}`, base-int64(time.Hour), base+int64(time.Hour), 100)
	if err != nil {
		t.Fatalf("QueryLogQLLines: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines for service=checkout, want 2", len(lines))
	}
	for _, l := range lines {
		if l.Service != "checkout" {
			t.Errorf("Service = %q, want checkout", l.Service)
		}
		if l.Body == "" {
			t.Errorf("empty Body in %+v", l)
		}
	}
	t.Logf("logql selector → %d lines; first body=%q", len(lines), lines[0].Body)
}
