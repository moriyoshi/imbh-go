package imbhgo

import (
	"bytes"
	"context"
	"testing"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// correlatedLog is one OTLP log record carrying a trace/span id, a severity, and attributes — the
// shape trace↔log correlation and attribute predicates filter on.
type correlatedLog struct {
	body     string
	traceID  []byte // 16 bytes
	spanID   []byte // 8 bytes
	severity logspb.SeverityNumber
	attrs    map[string]string
}

// makeCorrelatedLogsRequest marshals an OTLP ExportLogsServiceRequest (one resource, one scope, N
// records) with per-record trace_id/span_id/severity/attributes populated.
func makeCorrelatedLogsRequest(t *testing.T, service string, base int64, logs []correlatedLog) []byte {
	t.Helper()
	records := make([]*logspb.LogRecord, len(logs))
	for i, l := range logs {
		kvs := make([]*commonpb.KeyValue, 0, len(l.attrs))
		for k, v := range l.attrs {
			kvs = append(kvs, &commonpb.KeyValue{
				Key:   k,
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}},
			})
		}
		records[i] = &logspb.LogRecord{
			TimeUnixNano:   uint64(base + int64(i)),
			SeverityNumber: l.severity,
			Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: l.body}},
			TraceId:        l.traceID,
			SpanId:         l.spanID,
			Attributes:     kvs,
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

// TestLogQueryTraceCorrelationAndPredicates exercises the trace↔log correlation and attribute
// predicate fields on LogQuery: filtering by TraceID returns only that trace's log, and the
// SeverityAtLeast / AttrExists predicates narrow the result set as expected.
func TestLogQueryTraceCorrelationAndPredicates(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	traceA := bytes.Repeat([]byte{0xaa}, 16)
	spanA := bytes.Repeat([]byte{0x01}, 8)
	traceB := bytes.Repeat([]byte{0xbb}, 16)
	spanB := bytes.Repeat([]byte{0x02}, 8)

	req := makeCorrelatedLogsRequest(t, "checkout", base, []correlatedLog{
		// trace A: one info line with tenant attr.
		{body: "start A", traceID: traceA, spanID: spanA,
			severity: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
			attrs:    map[string]string{"tenant": "acme"}},
		// trace B: an info line and an error line, no tenant attr.
		{body: "start B", traceID: traceB, spanID: spanB,
			severity: logspb.SeverityNumber_SEVERITY_NUMBER_INFO},
		{body: "boom B", traceID: traceB, spanID: spanB,
			severity: logspb.SeverityNumber_SEVERITY_NUMBER_ERROR},
	})
	r, err := db.IngestOTLPLogs(req)
	if err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}
	if r.Accepted != 3 {
		t.Fatalf("Accepted = %d, want 3 (rejected=%d)", r.Accepted, r.Rejected)
	}

	start, end := base-int64(3_600_000_000_000), base+int64(3_600_000_000_000)

	// (1) TraceID filter: only trace A's single log should come back.
	byTrace, err := db.QueryLogsTyped(context.Background(), LogQuery{
		TraceID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Start:   start, End: end,
	})
	if err != nil {
		t.Fatalf("QueryLogsTyped(TraceID): %v", err)
	}
	if len(byTrace) != 1 {
		t.Fatalf("TraceID filter: got %d logs, want 1: %+v", len(byTrace), byTrace)
	}
	if !bytes.Equal(byTrace[0].TraceID, traceA) {
		t.Errorf("TraceID filter: returned trace %x, want %x", byTrace[0].TraceID, traceA)
	}
	if byTrace[0].Body != "start A" {
		t.Errorf("TraceID filter: body = %q, want %q", byTrace[0].Body, "start A")
	}

	// (2) SeverityAtLeast predicate: only the single ERROR (17) line qualifies.
	bySeverity, err := db.QueryLogsTyped(context.Background(), LogQuery{
		SeverityAtLeast: 17,
		Start:           start, End: end,
	})
	if err != nil {
		t.Fatalf("QueryLogsTyped(SeverityAtLeast): %v", err)
	}
	if len(bySeverity) != 1 {
		t.Fatalf("SeverityAtLeast filter: got %d logs, want 1: %+v", len(bySeverity), bySeverity)
	}
	if bySeverity[0].Body != "boom B" || bySeverity[0].Severity != 17 {
		t.Errorf("SeverityAtLeast filter: got body=%q sev=%d, want body=boom B sev=17",
			bySeverity[0].Body, bySeverity[0].Severity)
	}

	// (3) AttrExists predicate: only the record carrying the `tenant` attribute (trace A) qualifies.
	byAttr, err := db.QueryLogsTyped(context.Background(), LogQuery{
		AttrExists: []string{"tenant"},
		Start:      start, End: end,
	})
	if err != nil {
		t.Fatalf("QueryLogsTyped(AttrExists): %v", err)
	}
	if len(byAttr) != 1 {
		t.Fatalf("AttrExists filter: got %d logs, want 1: %+v", len(byAttr), byAttr)
	}
	if byAttr[0].Body != "start A" {
		t.Errorf("AttrExists filter: body = %q, want %q", byAttr[0].Body, "start A")
	}

	// Sanity: with no predicates all 3 logs come back (the filters above are actually narrowing).
	all, err := db.QueryLogsTyped(context.Background(), LogQuery{Start: start, End: end})
	if err != nil {
		t.Fatalf("QueryLogsTyped(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("unfiltered: got %d logs, want 3", len(all))
	}
}
