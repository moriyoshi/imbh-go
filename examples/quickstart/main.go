//go:build sable_extern_lib

// Command quickstart is a runnable tour of the imbh-go binding: open an in-memory database, ingest a
// little OpenTelemetry data, and query it three ways (SQL, typed logs, typed metrics).
//
// Build the static library first, then run with the sable_extern_lib tag:
//
//	make            # builds rust/target/release/libimbhgo.a
//	go run -tags sable_extern_lib ./examples/quickstart
//
// (In a real program the OTLP bytes come from your OpenTelemetry SDK's OTLP/HTTP exporter; here we
// build them by hand so the example is self-contained.)
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	imbhgo "github.com/moriyoshi/imbh-go"

	"github.com/apache/arrow-go/v18/arrow/array"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

func main() {
	db, err := imbhgo.OpenInMemory()
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// --- Ingest --------------------------------------------------------------------------------
	base := time.Now().UnixNano()
	mustIngestLogs(db, "checkout", []string{"request ok", "payment error: timeout"}, base)
	mustIngestLogs(db, "api", []string{"served /health"}, base)
	mustIngestGauge(db, "web", "cpu.utilization", []float64{0.21, 0.34, 0.55}, base)

	// --- 1. SQL (lazy, zero-copy Arrow) -------------------------------------------------------
	fmt.Println("== SQL: total log count ==")
	rows, err := db.Query(context.Background(), "SELECT count(*) AS n FROM logs")
	if err != nil {
		log.Fatal(err)
	}
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			log.Fatal(err)
		}
		if !ok {
			break
		}
		fmt.Printf("  %d logs\n", rec.Column(0).(*array.Int64).Value(0))
		rec.Release()
	}
	rows.Close()

	// --- 2. Typed log query, decoded to Go structs -------------------------------------------
	fmt.Println("== Logs matching \"error\" ==")
	entries, err := db.QueryLogsTyped(context.Background(), imbhgo.LogQuery{Match: "error"})
	if err != nil {
		log.Fatal(err)
	}
	for _, e := range entries {
		fmt.Printf("  [%s] %s\n", e.Service, e.Body)
	}

	// --- 3. Typed metric range → a Matrix ----------------------------------------------------
	fmt.Println("== Metric range: cpu.utilization ==")
	m, err := db.QueryMetricsTyped(context.Background(), imbhgo.MetricQuery{
		Metric: "cpu.utilization",
		Start:  base - int64(time.Hour),
		End:    base + int64(time.Hour),
		Step:   int64(time.Hour),
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range m.Series {
		for _, p := range s.Points {
			fmt.Printf("  labels=%v value=%.3f\n", s.Labels, p.V)
		}
	}
}

// --- tiny OTLP builders (stand in for an OTel SDK exporter) -----------------------------------

func serviceResource(service string) *resourcepb.Resource {
	return &resourcepb.Resource{Attributes: []*commonpb.KeyValue{{
		Key:   "service.name",
		Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}},
	}}}
}

func mustIngestLogs(db *imbhgo.DB, service string, bodies []string, base int64) {
	records := make([]*logspb.LogRecord, len(bodies))
	for i, body := range bodies {
		records[i] = &logspb.LogRecord{
			TimeUnixNano:   uint64(base + int64(i)),
			SeverityNumber: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
			SeverityText:   "INFO",
			Body:           &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: body}},
		}
	}
	req := &collogspb.ExportLogsServiceRequest{ResourceLogs: []*logspb.ResourceLogs{{
		Resource:  serviceResource(service),
		ScopeLogs: []*logspb.ScopeLogs{{LogRecords: records}},
	}}}
	b, err := proto.Marshal(req)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := db.IngestOTLPLogs(b); err != nil {
		log.Fatal(err)
	}
}

func mustIngestGauge(db *imbhgo.DB, service, metric string, values []float64, base int64) {
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
		log.Fatal(err)
	}
	if _, err := db.IngestOTLPMetrics(b); err != nil {
		log.Fatal(err)
	}
}
