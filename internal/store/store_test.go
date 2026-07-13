package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// sortByTimestamp must order newest-first, and must leave entries that share a
// timestamp in the order they were read. Query slices Offset/Limit off the sorted
// result, so an undefined order among ties means paging can skip or repeat a row.
func TestSortByTimestampOrdersNewestFirstAndKeepsTiesStable(t *testing.T) {
	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)

	entries := []models.LogEntry{
		{ID: "oldest", Timestamp: base},
		{ID: "tie-a", Timestamp: base.Add(time.Hour)},
		{ID: "newest", Timestamp: base.Add(2 * time.Hour)},
		{ID: "tie-b", Timestamp: base.Add(time.Hour)},
	}

	sortByTimestamp(entries)

	got := []string{}
	for _, e := range entries {
		got = append(got, e.ID)
	}
	want := []string{"newest", "tie-a", "tie-b", "oldest"}

	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sortByTimestamp = %v, want %v (newest first; tie-a before tie-b since it was read first)", got, want)
		}
	}
}

// The 2026-07-12 finding, pinned: an unscoped query must not cost quadratic time.
//
// sortByTimestamp used to be a hand-rolled selection sort, and Query runs it over
// every entry it materialised. The live 30-day window holds ~196k entries, so that
// was ~1.9e10 comparisons — a measured ~118s of CPU on a request that spends only
// ~2s reading all 88MB off disk. A bare GET /logs/group/<field> therefore burned
// two minutes of CPU, and kayushkin.com proxies that endpoint straight through.
//
// This asserts with time, which is usually a flaky way to pin a property — but the
// margin here is three orders of magnitude, not a factor of two. An O(n log n) sort
// of this many entries takes tens of milliseconds; the selection sort it replaced
// cannot physically finish in under a minute. Nothing lands in between, so no amount
// of load on the box makes this test ambiguous.
func TestSortByTimestampIsNotQuadratic(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a full 30-day window of entries")
	}

	const entryCount = 200_000 // ~ the live 30-day window
	const budget = 15 * time.Second

	// Reverse-sorted input: the worst case for the old sort, and it forces a real
	// permutation rather than letting an already-ordered slice off cheap.
	base := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	entries := make([]models.LogEntry, entryCount)
	for i := range entries {
		entries[i] = models.LogEntry{Timestamp: base.Add(time.Duration(i) * time.Millisecond)}
	}

	start := time.Now()
	sortByTimestamp(entries)
	elapsed := time.Since(start)

	if elapsed > budget {
		t.Fatalf("sorting %d entries took %v (budget %v) — sortByTimestamp is quadratic again; "+
			"an unscoped Query now burns minutes of CPU", entryCount, elapsed.Round(time.Millisecond), budget)
	}

	for i := 1; i < len(entries); i++ {
		if entries[i-1].Timestamp.Before(entries[i].Timestamp) {
			t.Fatalf("entry %d is older than entry %d: not sorted newest-first", i-1, i)
		}
	}
}

// seedCorpus writes n entries across a few day-dirs and returns a store over them.
// Every 7th entry shares a timestamp with its predecessor, so the tie handling the
// bounded selector has to reproduce is actually exercised.
func seedCorpus(t *testing.T, n int) *FileStore {
	t.Helper()

	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	base := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	ts := base
	for i := 0; i < n; i++ {
		if i%7 != 0 {
			ts = base.Add(time.Duration(i) * time.Second)
		} // else: reuse the previous timestamp, creating a tie
		entry := &models.LogEntry{
			ID:           fmt.Sprintf("entry-%06d", i),
			Timestamp:    ts,
			Orchestrator: []string{"scheduler", "openclaw", "inber", "si", "nightly-worker"}[i%5],
			Level:        []string{"info", "error"}[i%2],
			Type:         "outbound",
			Content:      map[string]any{"text": fmt.Sprintf("body of entry %d", i)},
		}
		if err := store.Write(entry); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}
	return store
}

// referenceQuery is what Query did before it learned to bound itself: materialise
// every match, stable-sort it, then slice. It is the definition the bounded path
// must reproduce exactly.
func referenceQuery(t *testing.T, s *FileStore, params models.QueryParams) []models.LogEntry {
	t.Helper()

	var all []models.LogEntry
	for _, dir := range s.getDirsToScan(params) {
		if err := s.scanDir(dir, params, func(e models.LogEntry) { all = append(all, e) }); err != nil {
			t.Fatalf("scanDir: %v", err)
		}
	}
	sortByTimestamp(all)

	if params.Offset > 0 {
		if params.Offset >= len(all) {
			return nil
		}
		all = all[params.Offset:]
	}
	if params.Limit > 0 && params.Limit < len(all) {
		all = all[:params.Limit]
	}
	return all
}

// A bounded query must return EXACTLY the rows that sorting the whole window and
// slicing would have returned — same rows, same order, including across ties.
//
// This is the contract that lets Query drop entries during the scan instead of
// materialising ~192k of them to hand back 100. Getting the tie-break wrong would
// not fail loudly; it would quietly reorder rows that share a timestamp, and a
// client paging through them would skip or repeat one. Every 7th seeded entry is
// a deliberate tie for that reason.
func TestBoundedQueryMatchesSortingEverything(t *testing.T) {
	store := seedCorpus(t, 5000)

	cases := []models.QueryParams{
		{Limit: 1},
		{Limit: 100},
		{Limit: 100, Offset: 50},
		{Limit: 3, Offset: 2},
		{Limit: 250, Offset: 1000},
		{Limit: 4999},
		{Limit: 5000},
		{Limit: 6000},                          // limit past the end
		{Limit: 10, Orchestrator: "scheduler"}, // filtered
		{Limit: 10, Level: "error", Offset: 3}, // filtered + offset
		{Limit: 100000},                        // the "no practical limit" shape Usage uses
	}

	for _, params := range cases {
		want := referenceQuery(t, store, params)

		got, err := store.Query(params)
		if err != nil {
			t.Fatalf("Query(%+v): %v", params, err)
		}

		if len(got) != len(want) {
			t.Fatalf("Query(%+v) returned %d entries, sorting everything returns %d", params, len(got), len(want))
		}
		for i := range want {
			if got[i].ID != want[i].ID {
				t.Fatalf("Query(%+v) row %d = %s, want %s (bounded selection diverged from the stable sort)",
					params, i, got[i].ID, want[i].ID)
			}
		}
	}
}

// The point of the bounded path: a bounded query must not retain the corpus.
//
// Query used to parse every entry in the window into one slice and sort all of it
// before slicing off Offset/Limit, so the DEFAULT `GET /logs` (Limit=100, no
// window) held ~192k live entries — ~300MB measured on the live corpus — to
// return a hundred rows. The cost of a request was set by how much history was on
// disk, not by what the request asked for, and logstack is proxied anonymously.
//
// Asserts on live heap after a forced GC, which measures retention rather than
// churn — that is the property, and it is what regresses if anyone reinstates the
// materialise-everything path. The ratio is deliberately generous (the true
// difference is ~500x here); nothing legitimate lands near it.
func TestBoundedQueryDoesNotRetainTheCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a corpus large enough to measure retention")
	}

	store := seedCorpus(t, 40_000)

	liveHeapOf := func(params models.QueryParams) uint64 {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)

		got, err := store.Query(params)
		if err != nil {
			t.Fatalf("Query: %v", err)
		}

		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(got) // the result must still be live when we measure

		if after.HeapAlloc < before.HeapAlloc {
			return 0
		}
		return after.HeapAlloc - before.HeapAlloc
	}

	bounded := liveHeapOf(models.QueryParams{Limit: 100})
	unbounded := liveHeapOf(models.QueryParams{})

	if bounded > unbounded/8 {
		t.Fatalf("Query(limit=100) retained %d bytes; the unbounded query over the same corpus retained %d. "+
			"A bounded query is materialising the whole window again.", bounded, unbounded)
	}
}

// Asking for a page past the end must return nothing, not the first page.
//
// The guard used to be `Offset > 0 && Offset < len(results)`, so an offset beyond
// the result count skipped the slicing entirely and re-served row 0 onwards. A
// client paging until it saw an empty page would never see one: it would loop over
// page 1 forever.
func TestOffsetPastTheEndReturnsNothing(t *testing.T) {
	store := seedCorpus(t, 50)

	got, err := store.Query(models.QueryParams{Limit: 10, Offset: 999})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("Query(offset=999) over 50 entries returned %d rows (first is %s); an offset past the end must return none",
			len(got), got[0].ID)
	}
}

// A line too long for the scan buffer must fail the query, not truncate it.
//
// bufio.Scanner stops returning tokens when a line exceeds its buffer, and that is
// indistinguishable from EOF unless Err() is checked — which scanFile did not do.
// One oversized line therefore hid every entry written after it in that file, and
// the query reported the truncated result as a complete one. No line in the live
// corpus is close to the 1MB cap today, which is exactly why this would go
// unnoticed the day one is.
func TestAnOversizedLineFailsTheQueryInsteadOfTruncatingIt(t *testing.T) {
	dir := t.TempDir()
	store, err := NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	day := filepath.Join(dir, time.Now().Format("2006-01-02"))
	if err := os.MkdirAll(day, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// One good entry, one line over the 1MB cap, then another good entry: the last
	// is the one silent truncation used to swallow.
	huge, err := json.Marshal(models.LogEntry{
		ID:        "oversized",
		Timestamp: time.Now(),
		Content:   map[string]any{"text": strings.Repeat("x", 1024*1024)},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	first, _ := json.Marshal(models.LogEntry{ID: "first", Timestamp: time.Now(), Content: "a"})
	last, _ := json.Marshal(models.LogEntry{ID: "after-the-oversized-line", Timestamp: time.Now(), Content: "b"})

	line := append(append(append(append(first, '\n'), huge...), '\n'), last...)
	if err := os.WriteFile(filepath.Join(day, "openclaw.jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = store.Query(models.QueryParams{})
	if err == nil {
		t.Fatal("Query returned no error over a file with an oversized line: it silently dropped every entry after it")
	}
	if !strings.Contains(err.Error(), "openclaw.jsonl") {
		t.Fatalf("Query error %q does not name the file it could not read", err)
	}
}

// Group must return its groups in the same order every time.
//
// It built them in a map and ranged over it, and Go randomises map iteration — so
// the same request reshuffled its groups between calls, under an operator who had
// changed nothing. Biggest bucket first, ties by key.
func TestGroupOrderIsDeterministic(t *testing.T) {
	store := seedCorpus(t, 300)

	var first []string
	for run := 0; run < 8; run++ {
		groups, err := store.Group(models.QueryParams{}, "orchestrator")
		if err != nil {
			t.Fatalf("Group: %v", err)
		}

		order := make([]string, len(groups))
		for i, g := range groups {
			order[i] = g.GroupKey
		}

		if run == 0 {
			first = order
			continue
		}
		for i := range first {
			if order[i] != first[i] {
				t.Fatalf("Group returned %v on run %d but %v on run 0: the group order is not stable", order, run, first)
			}
		}
	}

	// And the documented order: biggest bucket first.
	groups, err := store.Group(models.QueryParams{}, "orchestrator")
	if err != nil {
		t.Fatalf("Group: %v", err)
	}
	for i := 1; i < len(groups); i++ {
		if groups[i-1].Count < groups[i].Count {
			t.Fatalf("group %q (n=%d) precedes %q (n=%d): groups must come back biggest-first",
				groups[i-1].GroupKey, groups[i-1].Count, groups[i].GroupKey, groups[i].Count)
		}
	}
}
