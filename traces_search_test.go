package imbhgo

import (
	"context"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

// makeTraceRequestWithAttr builds one single-span trace per name, each span carrying a string span
// attribute attrKey=attrVals[i], so predicate filters (attr_exists / attr_in / attr_matches) can be
// exercised. Each span gets a distinct trace id (separate traces), mirroring makeTraceRequest.
func makeTraceRequestWithAttr(t *testing.T, service, attrKey string, names, attrVals []string, base int64) []byte {
	t.Helper()
	spans := make([]*tracepb.Span, len(names))
	for i := range names {
		tid := make([]byte, 16)
		sid := make([]byte, 8)
		tid[15] = byte(i + 1)
		sid[7] = byte(i + 1)
		spans[i] = &tracepb.Span{
			TraceId:           tid,
			SpanId:            sid,
			Name:              names[i],
			Kind:              tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: uint64(base + int64(i)),
			EndTimeUnixNano:   uint64(base + int64(i) + int64(time.Millisecond)),
			Status:            &tracepb.Status{},
			Attributes: []*commonpb.KeyValue{{
				Key:   attrKey,
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: attrVals[i]}},
			}},
		}
	}
	req := &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource:   serviceResource(service),
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: spans}},
	}}}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	return b
}

// TestSearchTracesAttrPredicates: spans carry a distinguishing span attribute; the new predicate
// fields (AttrExists / AttrIn / AttrMatches) narrow the result set.
func TestSearchTracesAttrPredicates(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	// Three traces: two with http.method=GET, one with http.method=POST.
	otlp := makeTraceRequestWithAttr(t, "gateway", "http.method",
		[]string{"GET /a", "POST /b", "GET /c"},
		[]string{"GET", "POST", "GET"}, base)
	if _, err := db.IngestOTLPTraces(otlp); err != nil {
		t.Fatalf("ingest traces: %v", err)
	}

	// AttrExists: every span carries http.method → all three match.
	exists, err := db.SearchTraces(context.Background(), TraceQuery{Service: "gateway", AttrExists: []string{"http.method"}})
	if err != nil {
		t.Fatalf("SearchTraces (attr_exists): %v", err)
	}
	if len(exists) != 3 {
		t.Fatalf("AttrExists[http.method] returned %d traces, want 3", len(exists))
	}

	// AttrExists on an absent key → nothing matches.
	none, err := db.SearchTraces(context.Background(), TraceQuery{Service: "gateway", AttrExists: []string{"db.system"}})
	if err != nil {
		t.Fatalf("SearchTraces (attr_exists absent): %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("AttrExists[db.system] returned %d traces, want 0", len(none))
	}

	// AttrIn: only the POST span matches.
	post, err := db.SearchTraces(context.Background(), TraceQuery{
		Service: "gateway",
		AttrIn:  map[string][]string{"http.method": {"POST"}},
	})
	if err != nil {
		t.Fatalf("SearchTraces (attr_in): %v", err)
	}
	if len(post) != 1 {
		t.Fatalf("AttrIn[http.method=POST] returned %d traces, want 1", len(post))
	}

	// AttrNotIn: exclude POST → the two GET traces remain.
	notPost, err := db.SearchTraces(context.Background(), TraceQuery{
		Service:   "gateway",
		AttrNotIn: map[string][]string{"http.method": {"POST"}},
	})
	if err != nil {
		t.Fatalf("SearchTraces (attr_not_in): %v", err)
	}
	if len(notPost) != 2 {
		t.Fatalf("AttrNotIn[http.method=POST] returned %d traces, want 2", len(notPost))
	}
}

// TestSearchTraces ingests three single-span traces for one service (one errored) and exercises the
// trace-search typed query: an unfiltered-by-anything-but-service search returns all three with hex
// ids and a span count, and the error flag round-trips; a Name filter narrows the result set.
func TestSearchTraces(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	// makeTraceRequest gives each span a distinct trace id, so these are three separate traces.
	otlp := makeTraceRequest(t, "checkout",
		[]string{"GET /a", "GET /b", "GET /c"},
		[]bool{false, true, false}, base)
	if _, err := db.IngestOTLPTraces(otlp); err != nil {
		t.Fatalf("ingest traces: %v", err)
	}

	got, err := db.SearchTraces(context.Background(), TraceQuery{Service: "checkout"})
	if err != nil {
		t.Fatalf("SearchTraces: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("SearchTraces returned %d traces, want 3", len(got))
	}
	var errored, clean int
	for _, s := range got {
		if s.TraceID == "" {
			t.Errorf("trace has empty TraceID: %+v", s)
		}
		if s.SpanCount < 1 {
			t.Errorf("trace %s has SpanCount %d, want >= 1", s.TraceID, s.SpanCount)
		}
		if s.Error {
			errored++
		} else {
			clean++
		}
	}
	if errored != 1 {
		t.Errorf("errored traces = %d, want 1", errored)
	}
	if clean != 2 {
		t.Errorf("clean traces = %d, want 2", clean)
	}

	// Narrowing: an exact Name filter restricts the result set to the single matching trace.
	narrowed, err := db.SearchTraces(context.Background(), TraceQuery{Service: "checkout", Name: "GET /a"})
	if err != nil {
		t.Fatalf("SearchTraces (name filter): %v", err)
	}
	if len(narrowed) != 1 {
		t.Fatalf("Name-filtered search returned %d traces, want 1", len(narrowed))
	}
	if narrowed[0].Error {
		t.Errorf("GET /a trace should be clean, got Error=true")
	}

	// Narrowing: Limit caps the number of returned rows.
	limited, err := db.SearchTraces(context.Background(), TraceQuery{Service: "checkout", Limit: 1})
	if err != nil {
		t.Fatalf("SearchTraces (limit): %v", err)
	}
	if len(limited) != 1 {
		t.Fatalf("Limit:1 search returned %d traces, want 1", len(limited))
	}
}
