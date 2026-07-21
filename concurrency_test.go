package imbhgo

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// drainCount fully drains a *Rows (summing NumRows across batches), always closing it, and returns
// the row count or the first error. Unlike countRows it never touches *testing.T, so it is safe to
// call from many goroutines concurrently. Leaking an open cursor holds an admission slot, so the
// defer Close is load-bearing under the concurrency test.
func drainCount(rows *Rows) (int, error) {
	defer rows.Close()
	total := 0
	for {
		rec, ok, err := rows.Next()
		if err != nil {
			return total, err
		}
		if !ok {
			break
		}
		total += int(rec.NumRows())
		rec.Release()
	}
	return total, rows.Err()
}

// TestConcurrentQueries hammers a single shared *DB from many goroutines to shake out races,
// deadlocks, and admission-slot exhaustion in the sable fusion (Go scheduler <-> tokio). The dataset
// is ingested once up front; the readers only query, and every opened *Rows is fully drained and
// Closed. Run under -race for it to be meaningful.
func TestConcurrentQueries(t *testing.T) {
	SetMaxInFlight(0) // unbounded: this test is about correctness under load, not backpressure
	db, err := OpenInMemory()
	if err != nil {
		t.Fatalf("OpenInMemory: %v", err)
	}
	defer db.Close()

	base := int64(1_700_000_000_000_000_000)
	const nLogs = 8
	bodies := make([]string, nLogs)
	for i := range bodies {
		bodies[i] = fmt.Sprintf("line-%d", i)
	}
	if _, err := db.IngestOTLPLogs(makeLogsRequest(t, "checkout", bodies)); err != nil {
		t.Fatalf("IngestOTLPLogs: %v", err)
	}
	if _, err := db.IngestOTLPMetrics(makeGaugeRequest(t, "checkout", "cpu.util", []float64{1, 2, 3}, base)); err != nil {
		t.Fatalf("IngestOTLPMetrics: %v", err)
	}
	if err := db.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Single-threaded baselines the concurrent readers must reproduce exactly.
	baseCount := countLogs(t, db)
	if baseCount != nLogs {
		t.Fatalf("baseline log count = %d, want %d", baseCount, nLogs)
	}
	baseTyped, err := db.QueryLogsTyped(context.Background(), LogQuery{Service: "checkout"})
	if err != nil {
		t.Fatalf("baseline QueryLogsTyped: %v", err)
	}
	if len(baseTyped) != nLogs {
		t.Fatalf("baseline typed logs = %d, want %d", len(baseTyped), nLogs)
	}

	const (
		goroutines = 48
		iterations = 60
	)
	var (
		wg      sync.WaitGroup
		errCnt  atomic.Int64
		firstMu sync.Mutex
		firstEr error
	)
	fail := func(format string, args ...any) {
		errCnt.Add(1)
		firstMu.Lock()
		if firstEr == nil {
			firstEr = fmt.Errorf(format, args...)
		}
		firstMu.Unlock()
	}

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				switch (g + i) % 3 {
				case 0: // raw SQL query, drained batch-by-batch
					rows, err := db.Query(context.Background(), "SELECT * FROM logs WHERE service = 'checkout'")
					if err != nil {
						fail("g%d i%d SQL Query: %v", g, i, err)
						continue
					}
					n, err := drainCount(rows)
					if err != nil {
						fail("g%d i%d SQL drain: %v", g, i, err)
						continue
					}
					if n != nLogs {
						fail("g%d i%d SQL rows = %d, want %d", g, i, n, nLogs)
					}
				case 1: // typed log query (its own zero-copy decode path)
					logs, err := db.QueryLogsTyped(context.Background(), LogQuery{Service: "checkout"})
					if err != nil {
						fail("g%d i%d QueryLogsTyped: %v", g, i, err)
						continue
					}
					if len(logs) != nLogs {
						fail("g%d i%d typed logs = %d, want %d", g, i, len(logs), nLogs)
					}
				case 2: // typed streaming query via QueryLogs -> *Rows
					rows, err := db.QueryLogs(context.Background(), LogQuery{Service: "checkout"})
					if err != nil {
						fail("g%d i%d QueryLogs: %v", g, i, err)
						continue
					}
					n, err := drainCount(rows)
					if err != nil {
						fail("g%d i%d QueryLogs drain: %v", g, i, err)
						continue
					}
					if n != nLogs {
						fail("g%d i%d QueryLogs rows = %d, want %d", g, i, n, nLogs)
					}
				}
			}
		}(g)
	}

	// Guard against a deadlock hanging the whole suite: fail loudly if the readers don't finish.
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatalf("concurrent queries did not finish within 30s (possible deadlock/slot exhaustion)")
	}

	if n := errCnt.Load(); n != 0 {
		t.Fatalf("%d concurrent-query failures; first: %v", n, firstEr)
	}
}
