package imbhgo

// compaction_schema_test.go — the imbh 0.6.0 compaction fix, exercised through the two knobs this
// binding exposes that can reach it: DbOptions.PromoteKeys and Compact(). Compaction concatenated a
// day partition's batches positionally against the *first* segment's schema, so changing the promoted
// key set between two seals in the same UTC day could panic, silently truncate the later segments'
// promoted columns, or — when the widths happened to match — silently merge two differently-named
// columns under the first segment's name. Two of the three failed quietly and wrote the result back.
//
// Both segment orders are covered: promoting a key between seals (the second segment is wider) and
// dropping one (the second is narrower). The assertions read the attributes back, not just the row
// count, since the mislabeling failure preserved the count.

import (
	"context"
	"strings"
	"testing"

	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
)

// sealedRunWithPromote opens `dir` promoting `promote`, ingests one log per body carrying `attrs`, and
// seals it into a segment before closing — one sealed segment per call, all in the same UTC day.
func sealedRunWithPromote(t *testing.T, dir string, promote []string, base int64, bodies []string, attrs map[string]string) {
	t.Helper()
	db, err := OpenWith(DbOptions{Path: dir, PromoteKeys: promote})
	if err != nil {
		t.Fatalf("OpenWith(promote %v): %v", promote, err)
	}
	defer db.Close()

	logs := make([]correlatedLog, len(bodies))
	for i, b := range bodies {
		logs[i] = correlatedLog{
			body:     b,
			severity: logspb.SeverityNumber_SEVERITY_NUMBER_INFO,
			attrs:    attrs,
		}
	}
	if _, err := db.IngestOTLPLogs(makeCorrelatedLogsRequest(t, "web", base, logs)); err != nil {
		t.Fatalf("IngestOTLPLogs(promote %v): %v", promote, err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush(promote %v): %v", promote, err)
	}
}

func TestCompactionAcrossAPromotedKeyChange(t *testing.T) {
	// Both runs stamp records inside the same UTC day, which is the partition compaction merges over.
	base := int64(1_700_000_000_000_000_000)

	cases := []struct {
		name          string
		firstPromote  []string
		secondPromote []string
		// wantCounts is per (key, value) after compaction. It differs between the two cases because
		// the *predicate* path depends on whether the key is promoted in the reopened database:
		// a promoted key reads the dictionary column, a non-promoted one reads the attributes JSON.
		wantCounts map[string]map[string]uint64
	}{
		// The later segment carries a promoted column the first does not: pre-0.6.0 this truncated it
		// away (narrow schema applied positionally to a wider batch) or panicked.
		//
		// `tenant` is promoted only for the second run, and imbh's null-fill is deliberately not a
		// back-fill (its ARCHITECTURE.md §6.1): a promoted column is projected from `attributes` at
		// ingest only, so the first run's rows keep a NULL `tenant` column through compaction and a
		// promoted-key predicate does not match them — exactly what the same query returned before
		// compaction, which is the property that makes compaction answer-preserving. Their value is
		// still in the attributes JSON, asserted separately below.
		{
			name: "promote_a_key_between_seals", firstPromote: []string{"region"}, secondPromote: []string{"region", "tenant"},
			wantCounts: map[string]map[string]uint64{
				"region": {"us": 2, "eu": 2},       // promoted for both runs: every row has the column
				"tenant": {"acme": 0, "globex": 2}, // promoted for the second run only
			},
		},
		// The reverse order, where the first segment is the wider one. `tenant` is not promoted in the
		// reopened database, so its predicate goes through `json_get_str(attributes, …)` and sees
		// every row regardless of which promote set sealed it.
		{
			name: "drop_a_key_between_seals", firstPromote: []string{"region", "tenant"}, secondPromote: []string{"region"},
			wantCounts: map[string]map[string]uint64{
				"region": {"us": 2, "eu": 2},
				"tenant": {"acme": 2, "globex": 2},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sealedRunWithPromote(t, dir, tc.firstPromote, base,
				[]string{"first-a", "first-b"}, map[string]string{"region": "us", "tenant": "acme"})
			sealedRunWithPromote(t, dir, tc.secondPromote, base+1000,
				[]string{"second-a", "second-b"}, map[string]string{"region": "eu", "tenant": "globex"})

			db, err := OpenWith(DbOptions{Path: dir, PromoteKeys: tc.secondPromote})
			if err != nil {
				t.Fatalf("reopen: %v", err)
			}
			defer db.Close()

			rep, err := db.Compact()
			if err != nil {
				t.Fatalf("Compact: %v", err)
			}
			if rep.SegmentsMerged < 2 {
				t.Fatalf("Compact merged %d segments; the fixture must produce >= 2 in one day partition (report %+v)", rep.SegmentsMerged, rep)
			}

			// Every row survives, with its own attribute values — not the other segment's, and not
			// truncated to NULL.
			entries, err := db.QueryLogsTyped(context.Background(), LogQuery{Limit: 100})
			if err != nil {
				t.Fatalf("QueryLogsTyped after compaction: %v", err)
			}
			if len(entries) != 4 {
				t.Fatalf("after compaction: %d rows, want 4", len(entries))
			}
			wantRegion := map[string]string{"first-a": "us", "first-b": "us", "second-a": "eu", "second-b": "eu"}
			wantTenant := map[string]string{"first-a": "acme", "first-b": "acme", "second-a": "globex", "second-b": "globex"}
			for _, e := range entries {
				region, ok := wantRegion[e.Body]
				if !ok {
					t.Errorf("unexpected body %q after compaction", e.Body)
					continue
				}
				if !strings.Contains(e.Attributes, `"region":"`+region+`"`) {
					t.Errorf("%s: attributes %s lost or mislabeled region=%s", e.Body, e.Attributes, region)
				}
				if tenant := wantTenant[e.Body]; !strings.Contains(e.Attributes, `"tenant":"`+tenant+`"`) {
					t.Errorf("%s: attributes %s lost or mislabeled tenant=%s", e.Body, e.Attributes, tenant)
				}
			}

			// The predicate path, which reads the promoted column rather than the attributes JSON.
			// Silent truncation or mislabeling in the merged segment shows up here as a wrong count.
			for key, want := range tc.wantCounts {
				for value, n := range want {
					got, err := db.CountLogs(context.Background(), LogQuery{AttrEq: map[string]string{key: value}})
					if err != nil {
						t.Fatalf("CountLogs(%s=%s): %v", key, value, err)
					}
					if got != n {
						t.Errorf("CountLogs(%s=%s) = %d after compaction, want %d", key, value, got, n)
					}
				}
			}
		})
	}
}
