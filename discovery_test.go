package imbhgo

import (
	"context"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

// makeGaugeRequestWithAttrs builds a single-metric OTLP gauge export where every data point carries
// the given attribute key=value, so the discovery surface (attr names/values, metric series) has a
// non-trivial label set to return.
func makeGaugeRequestWithAttrs(t *testing.T, service, metric, attrKey, attrVal string, values []float64, base int64) []byte {
	t.Helper()
	dps := make([]*metricspb.NumberDataPoint, len(values))
	for i, v := range values {
		dps[i] = &metricspb.NumberDataPoint{
			TimeUnixNano: uint64(base + int64(i)*int64(time.Second)),
			Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: v},
			Attributes: []*commonpb.KeyValue{{
				Key:   attrKey,
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: attrVal}},
			}},
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

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestMetricCatalogAndSeries ingests one gauge (with a data-point attribute) and asserts the metric
// shows up in the catalog with a kind, and that its series carries a non-empty label set.
func TestMetricCatalogAndSeries(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPMetrics(makeGaugeRequestWithAttrs(t, "web", "cpu.util", "host", "node-a", []float64{0.1, 0.2, 0.3}, base)); err != nil {
		t.Fatalf("ingest gauge: %v", err)
	}

	catalog, err := db.MetricCatalog(context.Background())
	if err != nil {
		t.Fatalf("MetricCatalog: %v", err)
	}
	var got *MetricInfo
	for i := range catalog {
		if catalog[i].Metric == "cpu.util" {
			got = &catalog[i]
			break
		}
	}
	if got == nil {
		t.Fatalf("MetricCatalog missing cpu.util; got %+v", catalog)
	}
	if got.Kind == "" {
		t.Errorf("cpu.util catalog entry has empty Kind: %+v", *got)
	}

	series, err := db.MetricSeries(context.Background(), "cpu.util")
	if err != nil {
		t.Fatalf("MetricSeries: %v", err)
	}
	if len(series) == 0 {
		t.Fatal("MetricSeries returned no label sets")
	}
	nonEmpty := false
	for _, s := range series {
		if s != "" && s != "{}" {
			nonEmpty = true
		}
	}
	if !nonEmpty {
		t.Errorf("MetricSeries returned only empty label sets: %q", series)
	}
	// The data-point attribute should surface as a canonical-JSON label set.
	if !contains(series, `{"host":"node-a"}`) {
		t.Errorf("MetricSeries = %q, want a set containing host=node-a", series)
	}
}

// TestAttrNamesAndValues asserts the attribute-discovery surface: the ingested data-point attribute
// key and the promoted service.name show up in AttrNames, and AttrValues resolves each.
func TestAttrNamesAndValues(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()
	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPMetrics(makeGaugeRequestWithAttrs(t, "web", "cpu.util", "host", "node-a", []float64{0.1, 0.2}, base)); err != nil {
		t.Fatalf("ingest gauge: %v", err)
	}

	names, err := db.AttrNames(context.Background())
	if err != nil {
		t.Fatalf("AttrNames: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("AttrNames returned nothing")
	}
	if !contains(names, "host") {
		t.Errorf("AttrNames = %q, want it to contain host", names)
	}
	if !contains(names, "service.name") {
		t.Errorf("AttrNames = %q, want it to contain service.name", names)
	}

	hostVals, err := db.AttrValues(context.Background(), "host")
	if err != nil {
		t.Fatalf("AttrValues(host): %v", err)
	}
	if !contains(hostVals, "node-a") {
		t.Errorf("AttrValues(host) = %q, want it to contain node-a", hostVals)
	}

	svcVals, err := db.AttrValues(context.Background(), "service.name")
	if err != nil {
		t.Fatalf("AttrValues(service.name): %v", err)
	}
	if !contains(svcVals, "web") {
		t.Errorf("AttrValues(service.name) = %q, want it to contain web", svcVals)
	}
}

// makeGaugeWithExemplarRequest builds a single-metric OTLP gauge whose one data point carries one
// exemplar (a trace/span link + value + a filtered attribute), mirroring imbh's own exemplar
// round-trip fixture. This is the metric shape MetricExemplars surfaces.
func makeGaugeWithExemplarRequest(t *testing.T, service, metric string, traceID []byte, spanID []byte, exTime uint64, exVal float64) []byte {
	t.Helper()
	ex := &metricspb.Exemplar{
		TimeUnixNano: exTime,
		Value:        &metricspb.Exemplar_AsDouble{AsDouble: exVal},
		TraceId:      traceID,
		SpanId:       spanID,
		FilteredAttributes: []*commonpb.KeyValue{{
			Key:   "sampler",
			Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "always_on"}},
		}},
	}
	dp := &metricspb.NumberDataPoint{
		TimeUnixNano: 1_700_000_000_000_000_000,
		Value:        &metricspb.NumberDataPoint_AsDouble{AsDouble: 42.0},
		Exemplars:    []*metricspb.Exemplar{ex},
	}
	req := &colmetricspb.ExportMetricsServiceRequest{ResourceMetrics: []*metricspb.ResourceMetrics{{
		Resource: serviceResource(service),
		ScopeMetrics: []*metricspb.ScopeMetrics{{Metrics: []*metricspb.Metric{{
			Name: metric,
			Data: &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: []*metricspb.NumberDataPoint{dp}}},
		}}}},
	}}}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal gauge with exemplar: %v", err)
	}
	return b
}

// TestMetricExemplars ingests a gauge whose point carries an exemplar and asserts MetricExemplars
// surfaces it with the trace/span hex, value, time, and canonical-JSON filtered attributes populated.
// It also asserts a metric with no exemplars returns an empty (non-error) slice.
func TestMetricExemplars(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	tid := make([]byte, 16)
	for i := range tid {
		tid[i] = 0xab
	}
	sid := make([]byte, 8)
	for i := range sid {
		sid[i] = 0xcd
	}
	if _, err := db.IngestOTLPMetrics(makeGaugeWithExemplarRequest(t, "web", "req.latency", tid, sid, 900, 41.5)); err != nil {
		t.Fatalf("ingest gauge with exemplar: %v", err)
	}

	exs, err := db.MetricExemplars(context.Background(), "req.latency")
	if err != nil {
		t.Fatalf("MetricExemplars: %v", err)
	}
	if len(exs) != 1 {
		t.Fatalf("MetricExemplars len = %d, want 1: %+v", len(exs), exs)
	}
	e := exs[0]
	if e.Time != 900 {
		t.Errorf("exemplar Time = %d, want 900", e.Time)
	}
	if e.Value != 41.5 {
		t.Errorf("exemplar Value = %v, want 41.5", e.Value)
	}
	// TraceId/SpanId round-trip as lowercase hex (0xab*16 / 0xcd*8).
	wantTrace := "abababababababababababababababab"
	wantSpan := "cdcdcdcdcdcdcdcd"
	if e.TraceID != wantTrace {
		t.Errorf("exemplar TraceID = %q, want %q", e.TraceID, wantTrace)
	}
	if e.SpanID != wantSpan {
		t.Errorf("exemplar SpanID = %q, want %q", e.SpanID, wantSpan)
	}
	if e.Attributes != `{"sampler":"always_on"}` {
		t.Errorf("exemplar Attributes = %q, want the canonical-JSON filtered attrs", e.Attributes)
	}

	// A gauge with no exemplars yields an empty, non-error slice (NULL trace/span never applies here).
	if _, err := db.IngestOTLPMetrics(makeGaugeRequestWithAttrs(t, "web", "cpu.util", "host", "node-a", []float64{0.1}, 1_700_000_000_000_000_000)); err != nil {
		t.Fatalf("ingest plain gauge: %v", err)
	}
	none, err := db.MetricExemplars(context.Background(), "cpu.util")
	if err != nil {
		t.Fatalf("MetricExemplars(cpu.util): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("MetricExemplars(cpu.util) = %+v, want empty (no exemplars)", none)
	}
}

// makeLogsAtTimes builds an OTLP logs export where record i is stamped times[i] and carries the
// attribute level=levels[i], so log-volume bucketing (by time) and volume_by (by attribute) both have
// something to split on.
func makeLogsAtTimes(t *testing.T, service string, times []int64, levels []string) []byte {
	t.Helper()
	records := make([]*logspb.LogRecord, len(times))
	for i := range times {
		records[i] = &logspb.LogRecord{
			TimeUnixNano:   uint64(times[i]),
			SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
			SeverityText:   "INFO",
			Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "msg"}},
			Attributes: []*commonpb.KeyValue{{
				Key:   "level",
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: levels[i]}},
			}},
		}
	}
	req := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  serviceResource(service),
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
	}}}
	b, err := proto.Marshal(req)
	if err != nil {
		t.Fatalf("marshal logs: %v", err)
	}
	return b
}

// TestLogVolumeAndVolumeBy ingests logs across two time buckets and two levels, then asserts LogVolume
// buckets sum to the ingested count and split across >1 bucket, and that LogVolumeBy(level) yields a
// per-level breakdown whose counts still sum to the total and whose labels are canonical JSON.
func TestLogVolumeAndVolumeBy(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	const step = int64(time.Minute) // 60s buckets
	// 5 records: three in bucket 0 (base + <1min), two in bucket 1 (base + >1min).
	times := []int64{
		base + 1,
		base + 2,
		base + 3,
		base + step + 1,
		base + step + 2,
	}
	levels := []string{"info", "warn", "info", "warn", "info"}
	if _, err := db.IngestOTLPLogs(makeLogsAtTimes(t, "checkout", times, levels)); err != nil {
		t.Fatalf("ingest logs: %v", err)
	}

	buckets, err := db.LogVolume(context.Background(), LogQuery{Service: "checkout"}, step)
	if err != nil {
		t.Fatalf("LogVolume: %v", err)
	}
	var total int64
	for _, b := range buckets {
		total += b.Count
		if b.Labels != "{}" {
			t.Errorf("LogVolume bucket carries labels %q, want {} (un-grouped)", b.Labels)
		}
	}
	if total != int64(len(times)) {
		t.Errorf("LogVolume counts sum = %d, want %d", total, len(times))
	}
	if len(buckets) < 2 {
		t.Errorf("LogVolume returned %d buckets, want >= 2 (records span two 60s buckets)", len(buckets))
	}

	byLevel, err := db.LogVolumeBy(context.Background(), LogQuery{Service: "checkout"}, step, []string{"level"})
	if err != nil {
		t.Fatalf("LogVolumeBy: %v", err)
	}
	var byTotal int64
	seenLevel := false
	for _, b := range byLevel {
		byTotal += b.Count
		if b.Labels == `{"level":"info"}` || b.Labels == `{"level":"warn"}` {
			seenLevel = true
		}
	}
	if byTotal != int64(len(times)) {
		t.Errorf("LogVolumeBy(level) counts sum = %d, want %d", byTotal, len(times))
	}
	if !seenLevel {
		t.Errorf("LogVolumeBy(level) labels = %+v, want a canonical {\"level\":...} set", byLevel)
	}
}
