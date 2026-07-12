package store

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kayushkin/logstack/models"
)

// GroupFieldsAreHandled pins GroupFields to getGroupKey.
//
// These two drifted apart once already: models.LogEntry's Source field was
// renamed to Orchestrator and getGroupKey was updated, but the API handler
// kept its own hardcoded copy of the field list saying "source". Because
// getGroupKey answers "unknown" for anything it doesn't recognise rather than
// failing, GET /api/v1/logs/group/source returned HTTP 200 with every log
// collapsed into a single "unknown" group, while group/orchestrator -- which
// the store did support -- was rejected with a 400.
//
// A field that reaches getGroupKey's default case is a field that silently
// lies about its results, so treat that as the failure it is.
func TestGroupFieldsAreHandled(t *testing.T) {
	entry := &models.LogEntry{
		Timestamp:    time.Now(),
		Orchestrator: "openclaw",
		Agent:        "brigid",
		Channel:      "discord",
		Model:        "claude-opus-4-8",
		Level:        "info",
		Type:         "message",
		SessionID:    "sess-1",
	}

	for _, field := range GroupFields {
		t.Run(field, func(t *testing.T) {
			key := getGroupKey(entry, field)
			if key == "unknown" {
				t.Errorf("getGroupKey has no case for %q, so grouping by it collapses every entry into one \"unknown\" bucket", field)
			}
			if key == "" {
				t.Errorf("getGroupKey(%q) returned an empty key for a fully-populated entry", field)
			}
		})
	}
}

// TestQueryDoesNotTakeTheIndexLock pins the invariant behind the log-sink
// stall: Query must scan the filesystem without holding mu.
//
// The stall was not a slow query -- it was a starved one. Query held
// mu.RLock() for the whole of an unscoped 30-day scan; an ingest Write then
// blocked on Lock(); and because Go's RWMutex stops admitting new readers as
// soon as a writer is waiting, every later read and write parked behind that
// writer too. One anonymous GET /api/v1/logs/group/<field> took the entire log
// sink down for ~2 minutes -- while /health, which never touches the store,
// went on answering 200.
//
// So this asserts the property directly rather than timing a scan (a timing
// test would be flaky, and would only fail on a corpus big enough to be slow).
// Holding mu.Lock() here stands in for an ingest Write in flight: Query must
// still complete. Restore the RLock in Query and this deadlocks -> fails.
func TestQueryDoesNotTakeTheIndexLock(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if err := store.Write(&models.LogEntry{
		Timestamp:    time.Now(),
		Orchestrator: "openclaw",
		Content:      "hello",
	}); err != nil {
		t.Fatalf("seed Write: %v", err)
	}

	// An ingest Write is mid-append and holds the writer lock.
	store.mu.Lock()
	defer store.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := store.Query(models.QueryParams{})
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Query: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Query blocked while a Write held mu: it is taking the index lock across a disk scan. " +
			"That is the bug that stalled the whole log sink for ~2 minutes -- a waiting writer parks " +
			"every subsequent reader behind it. Query reads only the filesystem; it must not take mu.")
	}
}

// TestWriteIsNotStarvedByConcurrentQueries is the end-to-end shape of the
// outage: ingest must keep landing while scans are in flight, and no entry may
// be lost or corrupted by the now-unsynchronised overlap. Run under -race.
func TestWriteIsNotStarvedByConcurrentQueries(t *testing.T) {
	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// Spread a corpus over several day-dirs so the scans have real work to do
	// and Write has to create new files while they run.
	now := time.Now()
	for day := 0; day < 5; day++ {
		for i := 0; i < 200; i++ {
			if err := store.Write(&models.LogEntry{
				Timestamp:    now.AddDate(0, 0, -day),
				Orchestrator: fmt.Sprintf("orch-%d", i%3),
				Content:      fmt.Sprintf("seed %d/%d", day, i),
				Level:        "info",
			}); err != nil {
				t.Fatalf("seed Write: %v", err)
			}
		}
	}

	stopScanning := make(chan struct{})
	var scanners sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		scanners.Add(1)
		go func() {
			defer scanners.Done()
			for {
				select {
				case <-stopScanning:
					return
				default:
					// Unscoped: the query shape that caused the outage.
					if _, err := store.Query(models.QueryParams{}); err != nil {
						t.Errorf("concurrent Query: %v", err)
						return
					}
				}
			}
		}()
	}

	const ingested = 50
	for i := 0; i < ingested; i++ {
		if err := store.Write(&models.LogEntry{
			Timestamp:    now,
			Orchestrator: "ingest",
			Content:      fmt.Sprintf("live %d", i),
			Level:        "info",
		}); err != nil {
			t.Fatalf("Write %d starved or failed while queries were in flight: %v", i, err)
		}
	}

	close(stopScanning)
	scanners.Wait()

	// Every ingested entry must be readable back -- a lock we removed must not
	// have been holding the file append together.
	got, err := store.Query(models.QueryParams{Orchestrator: "ingest"})
	if err != nil {
		t.Fatalf("Query after ingest: %v", err)
	}
	if len(got) != ingested {
		t.Errorf("read back %d entries, wrote %d: concurrent append lost or corrupted lines", len(got), ingested)
	}
}

// A field nobody supports must not be advertised as valid.
func TestIsGroupFieldRejectsUnknownFields(t *testing.T) {
	// "source" is the pre-rename name. It must not come back: the store groups
	// by "orchestrator" now, and re-adding "source" here would resurrect the
	// silent "unknown"-bucket bug.
	for _, field := range []string{"source", "", "nonsense", "Orchestrator"} {
		if IsGroupField(field) {
			t.Errorf("IsGroupField(%q) = true, but getGroupKey cannot group by it", field)
		}
	}

	for _, field := range GroupFields {
		if !IsGroupField(field) {
			t.Errorf("IsGroupField(%q) = false for a field in GroupFields", field)
		}
	}
}
