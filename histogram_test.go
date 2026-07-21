package imbhgo

import (
	"context"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// makeHistogramRequest builds an OTLP ExportMetricsServiceRequest with one explicit-bucket histogram
// data point (cumulative).
func makeHistogramRequest(t *testing.T, service, metric string, bounds []float64, counts []uint64, sum float64) []byte {
	t.Helper()
	var total uint64
	for _, c := range counts {
		total += c
	}
	dp := &metricspb.HistogramDataPoint{
		TimeUnixNano:   1_700_000_000_000_000_000,
		Count:          total,
		Sum:            proto.Float64(sum),
		BucketCounts:   counts,
		ExplicitBounds: bounds,
	}
	m := &metricspb.Metric{
		Name: metric,
		Data: &metricspb.Metric_Histogram{Histogram: &metricspb.Histogram{
			DataPoints:             []*metricspb.HistogramDataPoint{dp},
			AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE,
		}},
	}
	req := &colmetricspb.ExportMetricsServiceRequest{
		ResourceMetrics: []*metricspb.ResourceMetrics{{
			Resource: &resourcepb.Resource{
				Attributes: []*commonpb.KeyValue{{
					Key:   "service.name",
					Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}},
				}},
			},
			ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{m}}},
		}},
	}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal OTLP metrics: %v", err)
	}
	return b
}

// TestHistogramQuantileViaSQL proves the "differentiator" — a computed histogram quantile — is already
// reachable today through the existing zero-copy SQL path, via IMBH's histogram_quantile UDF. No typed
// API or second transport is needed for it.
func TestHistogramQuantileViaSQL(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	// 10 observations, all in the (2, 5] bucket → any quantile falls in (2, 5].
	otlp := makeHistogramRequest(t, "svc", "http.server.duration",
		[]float64{1, 2, 5, 10}, []uint64{0, 0, 10, 0, 0}, 30.0)
	r, err := db.IngestOTLPMetrics(otlp)
	if err != nil {
		t.Fatalf("IngestOTLPMetrics: %v", err)
	}
	if r.Accepted == 0 {
		t.Fatalf("no histogram points accepted (rejected=%d)", r.Rejected)
	}

	rows, err := db.Query(context.Background(),
		"SELECT histogram_quantile(0.95, explicit_bounds, bucket_counts) AS p95 "+
			"FROM metrics_histogram WHERE metric = 'http.server.duration'")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	defer rows.Close()

	var p95 float64
	got := false
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if !ok {
			break
		}
		col := rec.Column(0).(*array.Float64)
		for i := 0; i < int(rec.NumRows()); i++ {
			p95 = col.Value(i)
			got = true
		}
		rec.Release()
	}
	if !got {
		t.Fatal("no rows from histogram_quantile query")
	}
	if !(p95 > 2.0 && p95 <= 5.0) {
		t.Fatalf("p95 = %v, want in (2, 5] (the bucket holding all observations)", p95)
	}
	t.Logf("IMBH computed histogram_quantile(0.95) = %.3f via SQL through the zero-copy path", p95)
}
