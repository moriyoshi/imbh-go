package imbhgo

import (
	"context"
	"testing"
	"time"

	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func serviceResource(service string) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
		Key:   "service.name",
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}},
	}}}
}

func makeGaugeRequest(t *testing.T, service, metric string, values []float64, base int64) []byte {
	t.Helper()
	dps := make([]*metricspb.NumberDataPoint, len(values))
	for i, v := range values {
		dps[i] = &metricspb.NumberDataPoint{
			TimeUnixNano: uint64(base + int64(i)*int64(time.Second)),
			Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: v},
		}
	}
	req := &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: serviceResource(service),
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: metric,
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: dps}},
		}}}},
	}}}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal gauge: %v", err)
	}
	return b
}

// TestQueryMetricsTypedMatrix decodes a metric range query into a Matrix.
func TestQueryMetricsTypedMatrix(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "svc", "cpu.util", []float64{10, 20, 30}, base)); err != nil {
		t.Fatalf("ingest gauge: %v", err)
	}

	m, err := db.QueryMetricsTyped(context.Background(), MetricQuery{
		Metric: "cpu.util",
		Start:  base - int64(time.Hour),
		End:    base + int64(time.Hour),
		Step:   int64(time.Hour), // one wide bucket → one point
	})
	if err != nil {
		t.Fatalf("QueryMetricsTyped: %v", err)
	}
	if len(m.Series) != 1 {
		t.Fatalf("Series = %d, want 1", len(m.Series))
	}
	pts := m.Series[0].Points
	if len(pts) == 0 {
		t.Fatal("series has no points")
	}
	if pts[0].V < 10 || pts[0].V > 30 {
		t.Errorf("aggregated value = %v, want within [10,30]", pts[0].V)
	}
}

// TestQueryMetricInstant: a gauge with several points across time. instant() is Vector semantics —
// exactly one sample per series, whose value is the LAST point of the range series.
func TestQueryMetricInstant(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "svc", "cpu.util", []float64{10, 20, 30}, base)); err != nil {
		t.Fatalf("ingest gauge: %v", err)
	}

	q := MetricQuery{
		Metric: "cpu.util",
		Start:  base - int64(time.Hour),
		End:    base + int64(time.Hour),
		Step:   int64(time.Second), // one bucket per point → last bucket holds the last value
	}

	// Cross-check against the range: instant is the last point of the (single) range series.
	m, err := db.QueryMetricsTyped(context.Background(), q)
	if err != nil {
		t.Fatalf("QueryMetricsTyped: %v", err)
	}
	if len(m.Series) != 1 {
		t.Fatalf("range Series = %d, want 1", len(m.Series))
	}
	lastPt := m.Series[0].Points[len(m.Series[0].Points)-1]

	samples, err := db.QueryMetricInstant(context.Background(), q)
	if err != nil {
		t.Fatalf("QueryMetricInstant: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("instant samples = %d, want exactly 1 per series (Vector semantics)", len(samples))
	}
	if samples[0].Value != lastPt.V {
		t.Errorf("instant value = %v, want last range point %v", samples[0].Value, lastPt.V)
	}
	if samples[0].Value != 30 {
		t.Errorf("instant value = %v, want 30 (last ingested point)", samples[0].Value)
	}
	if samples[0].Labels == "" {
		t.Error("instant Labels is empty, want canonical JSON label set")
	}
	t.Logf("instant sample = %+v", samples[0])
}

func makeTraceRequest(t *testing.T, service string, names []string, errorFlags []bool, base int64) []byte {
	t.Helper()
	spans := make([]*tracepb.Span, len(names))
	for i := range names {
		st := &tracepb.Status{}
		if errorFlags[i] {
			st.Code = tracepb.Status_STATUS_CODE_ERROR
		}
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
			Status:            st,
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

// TestQuerySpanMetricsTypedRED decodes a span (RED) metrics query: 3 spans, 1 errored → calls=3, errors=1.
func TestQuerySpanMetricsTypedRED(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	otlp := makeTraceRequest(t, "checkout",
		[]string{"GET /a", "GET /b", "GET /c"},
		[]bool{false, true, false}, base)
	if _, err := db.IngestOTLPTraces(otlp); err != nil {
		t.Fatalf("ingest traces: %v", err)
	}

	pts, err := db.QuerySpanMetricsTyped(context.Background(), SpanMetricsQuery{
		Service: "checkout",
		Start:   base - int64(time.Hour),
		End:     base + int64(time.Hour),
		Step:    int64(time.Hour), // one bucket
	})
	if err != nil {
		t.Fatalf("QuerySpanMetricsTyped: %v", err)
	}
	var calls, errs uint64
	for _, p := range pts {
		calls += p.Calls
		errs += p.Errors
	}
	if calls != 3 || errs != 1 {
		t.Fatalf("calls=%d errors=%d, want calls=3 errors=1", calls, errs)
	}
}

// TestQueryMetricPointsTyped: raw (unaggregated) gauge samples via imbh's points_batches.
func TestQueryMetricPointsTyped(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "web", "cpu.util", []float64{0.1, 0.2, 0.3}, base)); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	pts, err := db.QueryMetricPointsTyped(context.Background(), MetricPointsQuery{
		Metric: "cpu.util",
		Start:  base - int64(time.Hour),
		End:    base + int64(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryMetricPointsTyped: %v", err)
	}
	if len(pts) != 3 {
		t.Fatalf("got %d raw points, want 3 (unaggregated)", len(pts))
	}
	var sum float64
	for _, p := range pts {
		if p.Metric != "cpu.util" {
			t.Errorf("Metric = %q", p.Metric)
		}
		sum += p.Value
	}
	t.Logf("raw points=%d sum=%.2f first=%+v", len(pts), sum, pts[0])
}
