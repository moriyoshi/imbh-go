package imbhgo

// observed_time_test.go — the arrival (observed-time) axis added in imbh 0.6.0: LogQuery.ObservedAfter
// and LogQuery.OrderBy. Every record carries two clocks, and they disagree in exactly the way that
// breaks a naive tailer: a record can be *emitted* before one already delivered and still *arrive*
// after it. These tests pin the filter, the ordering (including NULL placement), the four wire paths
// that share the log-query wire, and the follow loop the axis exists to make expressible.

import (
	"context"
	"strings"
	"testing"
	"time"

	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// observedLog is one OTLP log record with its two clocks set independently: `timeNs` is when the event
// happened, `observedNs` is when the collector received it. An `observedNs` of 0 is sent as OTLP's
// unset, which imbh stores as a NULL observed_time — the case both the filter and the ordering have to
// treat specially.
type observedLog struct {
	body       string
	timeNs     int64
	observedNs int64
}

// makeObservedLogsRequest marshals an OTLP ExportLogsServiceRequest whose records carry both clocks.
func makeObservedLogsRequest(t *testing.T, service string, logs []observedLog) []byte {
	t.Helper()
	records := make([]*logspb.LogRecord, len(logs))
	for i, l := range logs {
		records[i] = &logspb.LogRecord{
			TimeUnixNano:         uint64(l.timeNs),
			ObservedTimeUnixNano: uint64(l.observedNs),
			SeverityNumber:       logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
			SeverityText:         "INFO",
			Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: l.body}},
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

// bodiesOf renders the decoded entries' bodies in the order the query returned them.
func bodiesOf(entries []LogEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Body
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestLogObservedTimeAxisOrdersAndFilters pins the two halves of the axis against one fixture where the
// event order and the arrival order are deliberately reversed. Ordering by the wrong clock, or letting
// the NULL-arrival record land at the head of a backwards probe, is visible here as a different body
// sequence.
func TestLogObservedTimeAxisOrdersAndFilters(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	// Event order: late-arrival (10) → no-arrival (20) → early-arrival (30).
	// Arrival order: early-arrival (10) → late-arrival (30), with no-arrival unplaceable (NULL).
	if _, err := db.IngestOTLPLogs(makeObservedLogsRequest(t, "web", []observedLog{
		{body: "early-arrival", timeNs: base + 30, observedNs: base + 10},
		{body: "late-arrival", timeNs: base + 10, observedNs: base + 30},
		{body: "no-arrival", timeNs: base + 20, observedNs: 0},
	})); err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}

	// The decoded entry exposes the arrival clock, which is what lets a caller compute the next
	// watermark. A NULL observed_time decodes as 0.
	entries, err := db.QueryLogsTyped(context.Background(), LogQuery{})
	if err != nil {
		t.Fatalf("QueryLogsTyped: %v", err)
	}
	observed := map[string]int64{}
	for _, e := range entries {
		observed[e.Body] = e.ObservedTime
	}
	for body, want := range map[string]int64{
		"early-arrival": base + 10,
		"late-arrival":  base + 30,
		"no-arrival":    0,
	} {
		if got := observed[body]; got != want {
			t.Errorf("LogEntry(%q).ObservedTime = %d, want %d", body, got-base, want-base)
		}
	}

	// Default axis: event time. Unchanged by the new field being present on the wire.
	entries, err = db.QueryLogsTyped(context.Background(), LogQuery{})
	if err != nil {
		t.Fatalf("QueryLogsTyped(default order): %v", err)
	}
	if want := []string{"late-arrival", "no-arrival", "early-arrival"}; !equalStrings(bodiesOf(entries), want) {
		t.Errorf("default order = %v, want event order %v", bodiesOf(entries), want)
	}

	// Arrival axis, forward: the two placeable records ascending, the NULL one last.
	entries, err = db.QueryLogsTyped(context.Background(), LogQuery{OrderBy: LogOrderObservedTime})
	if err != nil {
		t.Fatalf("QueryLogsTyped(observed order): %v", err)
	}
	if want := []string{"early-arrival", "late-arrival", "no-arrival"}; !equalStrings(bodiesOf(entries), want) {
		t.Errorf("arrival order = %v, want %v", bodiesOf(entries), want)
	}

	// Arrival axis, backward: descending — but NULLs stay last, so a "newest arrival" probe never
	// hands back the record that has no arrival instant at all.
	entries, err = db.QueryLogsTyped(context.Background(), LogQuery{OrderBy: LogOrderObservedTime, Backward: true})
	if err != nil {
		t.Fatalf("QueryLogsTyped(observed order, backward): %v", err)
	}
	if want := []string{"late-arrival", "early-arrival", "no-arrival"}; !equalStrings(bodiesOf(entries), want) {
		t.Errorf("backward arrival order = %v, want NULLs last in both directions: %v", bodiesOf(entries), want)
	}

	// ObservedAfter is a *strict* lower bound on arrival, and excludes the NULL-arrival record (SQL
	// NULL > t is unknown) — a record that cannot be placed against a watermark is left out rather
	// than replayed on every poll.
	entries, err = db.QueryLogsTyped(context.Background(), LogQuery{ObservedAfter: base + 10})
	if err != nil {
		t.Fatalf("QueryLogsTyped(observed_after): %v", err)
	}
	if want := []string{"late-arrival"}; !equalStrings(bodiesOf(entries), want) {
		t.Errorf("ObservedAfter(base+10) = %v, want %v (strict bound, NULL arrival excluded)", bodiesOf(entries), want)
	}

	// The filter rides the shared log-query wire, so the count and volume paths honor it too.
	n, err := db.CountLogs(context.Background(), LogQuery{ObservedAfter: base + 10})
	if err != nil {
		t.Fatalf("CountLogs(observed_after): %v", err)
	}
	if n != 1 {
		t.Errorf("CountLogs(ObservedAfter) = %d, want 1", n)
	}
	buckets, err := db.LogVolume(context.Background(), LogQuery{ObservedAfter: base + 10}, int64(time.Minute))
	if err != nil {
		t.Fatalf("LogVolume(observed_after): %v", err)
	}
	var total int64
	for _, b := range buckets {
		total += b.Count
	}
	if total != 1 {
		t.Errorf("LogVolume(ObservedAfter) total = %d, want 1", total)
	}

	// The paged path shares the wire as well: one page, ordered by arrival.
	page, err := db.QueryLogPage(context.Background(), LogQuery{OrderBy: LogOrderObservedTime, Limit: 2}, nil)
	if err != nil {
		t.Fatalf("QueryLogPage(observed order): %v", err)
	}
	if want := []string{"early-arrival", "late-arrival"}; !equalStrings(bodiesOf(page.Entries), want) {
		t.Errorf("paged arrival order = %v, want %v", bodiesOf(page.Entries), want)
	}
}

// TestLogTailerOnArrivalDeliversALateRecordExactlyOnce is the regression this axis exists for. A
// follow loop watermarked on *event* time silently drops a record that arrives after a newer-stamped
// one; the same loop on the arrival clock delivers it exactly once. Both halves are asserted, so the
// test still says what the axis is for if the arrival half ever regresses to matching the event half.
func TestLogTailerOnArrivalDeliversALateRecordExactlyOnce(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	ctx := context.Background()

	// Poll 1: two records whose clocks agree.
	if _, err := db.IngestOTLPLogs(makeObservedLogsRequest(t, "web", []observedLog{
		{body: "first", timeNs: base + 10, observedNs: base + 10},
		{body: "second", timeNs: base + 20, observedNs: base + 20},
	})); err != nil {
		t.Fatalf("IngestOTLPLogs(round 1): %v", err)
	}

	var watermark int64 // last arrival instant delivered; the loop's only state
	var delivered []string
	poll := func() {
		t.Helper()
		entries, err := db.QueryLogsTyped(ctx, LogQuery{
			ObservedAfter: watermark,
			OrderBy:       LogOrderObservedTime,
			Limit:         100,
		})
		if err != nil {
			t.Fatalf("tail poll: %v", err)
		}
		for _, e := range entries {
			delivered = append(delivered, e.Body)
			if e.ObservedTime > watermark {
				watermark = e.ObservedTime
			}
		}
	}

	poll()
	if want := []string{"first", "second"}; !equalStrings(delivered, want) {
		t.Fatalf("poll 1 delivered %v, want %v", delivered, want)
	}
	if watermark != base+20 {
		t.Fatalf("watermark after poll 1 = %d, want base+20", watermark-base)
	}

	// Poll 2: a record emitted *before* both of the above, but arriving after them — a line whose own
	// timestamp was trusted, or simply one batch interval of ingest lag.
	if _, err := db.IngestOTLPLogs(makeObservedLogsRequest(t, "web", []observedLog{
		{body: "late", timeNs: base + 5, observedNs: base + 30},
	})); err != nil {
		t.Fatalf("IngestOTLPLogs(round 2): %v", err)
	}

	// The event-time loop's watermark is the newest event time it delivered (base+20). Nothing with a
	// *later* event time exists, so it hands back nothing and "late" is lost for good.
	missed, err := db.QueryLogsTyped(ctx, LogQuery{Start: base + 21, Limit: 100})
	if err != nil {
		t.Fatalf("event-time poll: %v", err)
	}
	if len(missed) != 0 {
		t.Fatalf("event-time watermark returned %v; the fixture must keep the late record behind it", bodiesOf(missed))
	}

	poll()
	if want := []string{"first", "second", "late"}; !equalStrings(delivered, want) {
		t.Fatalf("poll 2 delivered %v, want %v (the late arrival, once)", delivered, want)
	}
	if watermark != base+30 {
		t.Fatalf("watermark after poll 2 = %d, want base+30", watermark-base)
	}

	// Poll 3: nothing new. The strict bound is what keeps the last delivered record from repeating.
	poll()
	if want := []string{"first", "second", "late"}; !equalStrings(delivered, want) {
		t.Fatalf("poll 3 delivered %v, want no repeats: %v", delivered, want)
	}
}

// TestLogQueryRejectsUnknownOrderAxis: an unrecognized OrderBy is an error, not a silent fallback to
// event time — a typo'd axis in a follow loop would otherwise reintroduce the dropped-record bug
// above with no signal. Asserted on all four surfaces that share the log-query wire.
func TestLogQueryRejectsUnknownOrderAxis(t *testing.T) {
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "web", []string{"alpha"})); err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}

	bad := LogQuery{OrderBy: LogOrder("arrival")}
	ctx := context.Background()

	check := func(what string, err error) {
		t.Helper()
		if err == nil {
			t.Errorf("%s with an unknown order axis succeeded; want an error", what)
			return
		}
		if !strings.Contains(err.Error(), "arrival") {
			t.Errorf("%s error does not name the rejected axis: %v", what, err)
		}
	}

	_, err = db.QueryLogsTyped(ctx, bad)
	check("QueryLogsTyped", err)
	_, err = db.CountLogs(ctx, bad)
	check("CountLogs", err)
	_, err = db.QueryLogPage(ctx, bad, nil)
	check("QueryLogPage", err)
	_, err = db.LogVolume(ctx, bad, int64(time.Minute))
	check("LogVolume", err)
}
