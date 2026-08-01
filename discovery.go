package imbhgo

// discovery.go — read-only metadata-discovery queries (the catalog surface). Each mirrors one of
// IMBH's async discovery APIs (`attrs().names`/`values`, `metrics().catalog`/`series`): a single
// Arrow batch is streamed back over the same Rows path as every other query and decoded here into a
// plain Go slice. These are catalog lookups — no time range, no window.

import (
	"context"
	"encoding/json"
)

// Op ids shared with rust/src/lib.rs.
const (
	opAttrNames       uint32 = 15
	opAttrValues      uint32 = 16
	opMetricCatalog   uint32 = 17
	opMetricSeries    uint32 = 18
	opMetricExemplars uint32 = 19
	opLogVolume       uint32 = 20
)

// MetricInfo is one metric's catalog entry: its name, unit, temporality ("" when the kind carries
// none, e.g. summaries), and kind ("gauge" | "sum" | "histogram" | ...).
type MetricInfo struct {
	Metric      string
	Unit        string
	Temporality string
	Kind        string
}

// AttrNames returns every distinct attribute/label key present on any signal (logs, spans, metrics),
// plus "service.name" when any record carries a service. Sorted.
func (db *DB) AttrNames(ctx context.Context) ([]string, error) {
	rows, err := db.openStream(ctx, opAttrNames, nil)
	if err != nil {
		return nil, err
	}
	return collectStringColumn(rows, "name")
}

// AttrValues returns the distinct string values for one attribute key across every signal, sorted.
func (db *DB) AttrValues(ctx context.Context, key string) ([]string, error) {
	b, err := json.Marshal(struct {
		Key string `json:"key"`
	}{Key: key})
	if err != nil {
		return nil, err
	}
	rows, err := db.openStream(ctx, opAttrValues, b)
	if err != nil {
		return nil, err
	}
	return collectStringColumn(rows, "value")
}

// MetricCatalog returns the metric catalog — one MetricInfo per stored metric.
func (db *DB) MetricCatalog(ctx context.Context) ([]MetricInfo, error) {
	rows, err := db.openStream(ctx, opMetricCatalog, nil)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetricInfo
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		cols := columnsByName(rec)
		n := int(rec.NumRows())
		for i := 0; i < n; i++ {
			out = append(out, MetricInfo{
				Metric:      stringAt(cols["metric"], i),
				Unit:        stringAt(cols["unit"], i),
				Temporality: stringAt(cols["temporality"], i),
				Kind:        stringAt(cols["kind"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}

// MetricSeries returns the distinct label sets (series) carrying a metric, each rendered as its
// canonical JSON object string. Resource-level dimensions like service are separate axes and are not
// folded in.
func (db *DB) MetricSeries(ctx context.Context, metric string) ([]string, error) {
	b, err := json.Marshal(struct {
		Metric string `json:"metric"`
	}{Metric: metric})
	if err != nil {
		return nil, err
	}
	rows, err := db.openStream(ctx, opMetricSeries, b)
	if err != nil {
		return nil, err
	}
	return collectStringColumn(rows, "labels")
}

// Exemplar is one OTLP exemplar surfaced from a metric point — the trace link for metric→trace
// drill-down. TraceID/SpanID are "" when the exemplar carries none. Attributes is the exemplar's
// filtered attributes as canonical JSON ("" when none).
type Exemplar struct {
	Time       int64
	Value      float64
	TraceID    string
	SpanID     string
	Attributes string
}

// MetricExemplars returns every exemplar recorded for a metric. Exemplars are carried by histogram/
// exponential-histogram (and gauge/sum) points; a metric with none yields an empty slice.
func (db *DB) MetricExemplars(ctx context.Context, metric string) ([]Exemplar, error) {
	b, err := json.Marshal(struct {
		Metric string `json:"metric"`
	}{Metric: metric})
	if err != nil {
		return nil, err
	}
	rows, err := db.openStream(ctx, opMetricExemplars, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Exemplar
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		cols := columnsByName(rec)
		n := int(rec.NumRows())
		for i := 0; i < n; i++ {
			out = append(out, Exemplar{
				Time:       int64At(cols["time"], i),
				Value:      float64At(cols["value"], i),
				TraceID:    stringAt(cols["trace_id"], i), // "" when the column is NULL
				SpanID:     stringAt(cols["span_id"], i),  // "" when the column is NULL
				Attributes: stringAt(cols["attributes"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}

// VolumeBucket is one time bucket of a log-volume query: the bucket start (unix nanos), the label set
// identifying this bucket's series as canonical JSON ("{}" when un-grouped), and the record count.
type VolumeBucket struct {
	Time   int64
	Labels string
	Count  int64
}

// logVolumeRequest is the wire form for OP_LOG_VOLUME: the log-query filter (flattened) plus the
// bucket width and the optional group-by keys. Defined here so query.go's LogQuery is left untouched.
type logVolumeRequest struct {
	LogQuery
	StepNs  int64    `json:"step_ns"`
	GroupBy []string `json:"group_by,omitempty"`
}

// LogVolume returns log record counts per stepNs-sized time bucket over the filter q. The bucket start
// is floor(time/stepNs)*stepNs in unix nanos; buckets carry no labels (Labels == "{}").
func (db *DB) LogVolume(ctx context.Context, q LogQuery, stepNs int64) ([]VolumeBucket, error) {
	return db.LogVolumeBy(ctx, q, stepNs, nil)
}

// LogVolumeBy is LogVolume broken down by the given attribute keys — counts per (step-bucket, label
// set), each bucket carrying its Labels as canonical JSON. Empty groupBy is equivalent to LogVolume.
//
// A key may name a record attribute or the service: "service.name" (the OTel resource key) and
// "service" (the column it is lifted into at ingest) both split per service. Requires imbh 0.3.0 —
// before that, either spelling silently collapsed the breakdown into one empty-labelled bucket set.
func (db *DB) LogVolumeBy(ctx context.Context, q LogQuery, stepNs int64, groupBy []string) ([]VolumeBucket, error) {
	b, err := json.Marshal(logVolumeRequest{LogQuery: q, StepNs: stepNs, GroupBy: groupBy})
	if err != nil {
		return nil, err
	}
	rows, err := db.openStream(ctx, opLogVolume, b)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VolumeBucket
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		cols := columnsByName(rec)
		n := int(rec.NumRows())
		for i := 0; i < n; i++ {
			out = append(out, VolumeBucket{
				Time:   int64At(cols["bucket_time"], i),
				Labels: stringAt(cols["labels"], i),
				Count:  int64At(cols["count"], i),
			})
		}
		rec.Release()
	}
	return out, nil
}

// collectStringColumn drains a single-column string result into a []string, copying each value out
// (via stringAt's strings.Clone) so it outlives the batch's Release. Closes rows before returning.
func collectStringColumn(rows *Rows, col string) ([]string, error) {
	defer rows.Close()
	var out []string
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return nil, err
		}
		if !ok {
			break
		}
		cols := columnsByName(rec)
		c := cols[col]
		n := int(rec.NumRows())
		for i := 0; i < n; i++ {
			out = append(out, stringAt(c, i))
		}
		rec.Release()
	}
	return out, nil
}
