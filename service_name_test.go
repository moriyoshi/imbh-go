package imbhgo

import (
	"context"
	"testing"
	"time"
)

// service.name is an OTel *resource* attribute that imbh lifts into the built-in `service` column at
// ingest; it is never a record `attributes` entry. Before imbh 0.3.0 every typed group-by / attribute
// predicate resolved it through `json_get_str(attributes, 'service.name')`, i.e. NULL on every row —
// so grouping collapsed all services into one empty-labelled series with the counts merged, and
// filtering on it matched nothing. Both failures were silent (a missing attribute is a legitimate
// NULL). imbh 0.3.0 resolves both spellings — `service.name` (the OTel key) and `service` (the column
// name) — to the column, in the single funnel every builder shares, so the binding's LogVolumeBy /
// MetricQuery.GroupBy / SpanMetricsQuery.GroupBy paths inherit the fix with no glue change.
//
// These are the gates for that: each one needs *two* services, since a single-service fixture cannot
// tell a working breakdown from the collapsed one.

// TestLogVolumeByServiceName pins the log-volume breakdown: one bucket series per service, under both
// spellings, with the counts landing on the right service instead of merged under "".
func TestLogVolumeByServiceName(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	const step = int64(time.Hour) // one wide bucket → the only split is the service
	if _, err := db.IngestOTLPLogs(makeLogsAtTimes(t, "cart",
		[]int64{base + 1, base + 2}, []string{"info", "warn"})); err != nil {
		t.Fatalf("ingest cart logs: %v", err)
	}
	if _, err := db.IngestOTLPLogs(makeLogsAtTimes(t, "checkout",
		[]int64{base + 3}, []string{"info"})); err != nil {
		t.Fatalf("ingest checkout logs: %v", err)
	}

	// Both spellings group the same way.
	for _, key := range []string{"service.name", "service"} {
		buckets, err := db.LogVolumeBy(context.Background(), LogQuery{}, step, []string{key})
		if err != nil {
			t.Fatalf("LogVolumeBy(%s): %v", key, err)
		}
		got := map[string]int64{}
		for _, b := range buckets {
			got[b.Labels] += b.Count
		}
		want := map[string]int64{
			`{"` + key + `":"cart"}`:     2,
			`{"` + key + `":"checkout"}`: 1,
		}
		if len(got) != len(want) {
			t.Errorf("LogVolumeBy(%s) label sets = %+v, want one per service %+v", key, got, want)
		}
		for label, n := range want {
			if got[label] != n {
				t.Errorf("LogVolumeBy(%s)[%s] = %d, want %d (all buckets: %+v)", key, label, got[label], n, got)
			}
		}
	}

	// The same key as an attribute *filter* agrees with the breakdown (it used to match nothing).
	n, err := db.CountLogs(context.Background(), LogQuery{AttrEq: map[string]string{"service.name": "cart"}})
	if err != nil {
		t.Fatalf("CountLogs(attr_eq service.name): %v", err)
	}
	if n != 2 {
		t.Errorf("CountLogs(attr_eq service.name=cart) = %d, want 2", n)
	}
	// And AttrExists sees it, rather than treating every row as missing the key.
	n, err = db.CountLogs(context.Background(), LogQuery{AttrExists: []string{"service.name"}})
	if err != nil {
		t.Fatalf("CountLogs(attr_exists service.name): %v", err)
	}
	if n != 3 {
		t.Errorf("CountLogs(attr_exists service.name) = %d, want 3 (every row has a service)", n)
	}
}

// TestQueryMetricsGroupByServiceName pins the metric range query's group-by: two services emitting the
// same metric name must come back as two series, not one merged series labelled "".
func TestQueryMetricsGroupByServiceName(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "cart", "cpu.util", []float64{10, 10}, base)); err != nil {
		t.Fatalf("ingest cart gauge: %v", err)
	}
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "checkout", "cpu.util", []float64{50, 50}, base)); err != nil {
		t.Fatalf("ingest checkout gauge: %v", err)
	}

	m, err := db.QueryMetricsTyped(context.Background(), MetricQuery{
		Metric:  "cpu.util",
		Start:   base - int64(time.Hour),
		End:     base + int64(time.Hour),
		Step:    int64(time.Hour), // one wide bucket → one point per series
		GroupBy: []string{"service.name"},
	})
	if err != nil {
		t.Fatalf("QueryMetricsTyped: %v", err)
	}
	byService := map[string]float64{}
	for _, s := range m.Series {
		if len(s.Points) == 0 {
			t.Fatalf("series %+v has no points", s.Labels)
		}
		byService[s.Labels["service.name"]] = s.Points[0].V
	}
	if len(byService) != 2 {
		t.Fatalf("series by service.name = %+v, want one per service (cart, checkout)", byService)
	}
	if byService["cart"] != 10 || byService["checkout"] != 50 {
		t.Errorf("series values = %+v, want cart=10 checkout=50 (merged would average to 30)", byService)
	}
}

// TestSpanMetricsGroupByServiceName pins the RED breakdown: calls attributed per service, not summed
// into one unlabelled row.
func TestSpanMetricsGroupByServiceName(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	if _, err := db.IngestOTLPTraces(makeTraceRequest(t, "cart",
		[]string{"GET /cart", "GET /cart/items"}, []bool{false, false}, base)); err != nil {
		t.Fatalf("ingest cart trace: %v", err)
	}
	if _, err := db.IngestOTLPTraces(makeTraceRequest(t, "checkout",
		[]string{"POST /checkout"}, []bool{true}, base)); err != nil {
		t.Fatalf("ingest checkout trace: %v", err)
	}

	pts, err := db.QuerySpanMetricsTyped(context.Background(), SpanMetricsQuery{
		Start:   base - int64(time.Hour),
		End:     base + int64(time.Hour),
		Step:    int64(time.Hour), // one wide bucket → the only split is the service
		GroupBy: []string{"service.name"},
	})
	if err != nil {
		t.Fatalf("QuerySpanMetricsTyped: %v", err)
	}
	calls := map[string]uint64{}
	errs := map[string]uint64{}
	for _, p := range pts {
		calls[p.Labels["service.name"]] += p.Calls
		errs[p.Labels["service.name"]] += p.Errors
	}
	if len(calls) != 2 {
		t.Fatalf("span metrics by service.name = %+v, want one label set per service", calls)
	}
	if calls["cart"] != 2 || calls["checkout"] != 1 {
		t.Errorf("calls = %+v, want cart=2 checkout=1", calls)
	}
	if errs["cart"] != 0 || errs["checkout"] != 1 {
		t.Errorf("errors = %+v, want cart=0 checkout=1", errs)
	}
}
