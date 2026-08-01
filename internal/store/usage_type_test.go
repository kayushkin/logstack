package store

import (
	"testing"
	"time"

	"github.com/kayushkin/logstack/models"
)

// writeUsageEntry files one assistant turn under the given type, with a Stats
// block as complete as a producer can make it.
func writeUsageEntry(t *testing.T, s *FileStore, id, entryType string, at time.Time) {
	t.Helper()

	entry := &models.LogEntry{
		ID:           id,
		Timestamp:    at,
		Orchestrator: "inber",
		Agent:        "claxon",
		SessionID:    "session-1",
		Model:        "claude-sonnet-4-5",
		Level:        models.LevelInfo,
		Type:         entryType,
		Content:      "the assistant's reply, as a plain string",
		Stats: &models.TurnStats{
			InputTokens:         100,
			OutputTokens:        20,
			CacheReadTokens:     9000,
			CacheCreationTokens: 500,
			Model:               "claude-sonnet-4-5",
		},
	}
	if err := s.Write(entry); err != nil {
		t.Fatalf("Write: %v", err)
	}
}

// A complete Stats block earns a producer nothing unless the entry is also
// filed as models.TypeOutbound.
//
// Usage and MaxUsage both query Type "outbound" and nothing else, so an
// assistant turn filed as TypeMessage is not "counted with zero tokens", it is
// never read at all. inber filed every assistant turn as TypeMessage and so
// contributed no usage for three months while looking, from its own side, like
// a healthy producer: it built an entry, posted it, got a 201, and the entry
// sat on disk in a bucket no reader selects.
//
// The type is therefore part of the usage contract, not decoration, which is
// why TypeInbound and TypeOutbound are constants rather than the bare strings
// they used to be in four places.
func TestOnlyOutboundEntriesReachUsage(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)

	for _, tc := range []struct {
		entryType string
		counted   bool
	}{
		{models.TypeOutbound, true},
		{models.TypeMessage, false},
		{models.TypeInbound, false},
		{models.TypeToolCall, false},
		{models.TypeLifecycle, false},
	} {
		t.Run(tc.entryType, func(t *testing.T) {
			store, err := NewFileStore(t.TempDir())
			if err != nil {
				t.Fatalf("NewFileStore: %v", err)
			}
			writeUsageEntry(t, store, "entry-1", tc.entryType, at)

			usage, err := store.Usage(at.Add(-time.Hour))
			if err != nil {
				t.Fatalf("Usage: %v", err)
			}
			if got := len(usage) > 0; got != tc.counted {
				t.Fatalf("Usage counted=%v for type %q, want %v", got, tc.entryType, tc.counted)
			}

			maxUsage, err := store.MaxUsage(at.Add(-time.Hour), at.Add(time.Hour))
			if err != nil {
				t.Fatalf("MaxUsage: %v", err)
			}
			if got := maxUsage.Totals.APICalls > 0; got != tc.counted {
				t.Fatalf("MaxUsage counted=%v for type %q, want %v", got, tc.entryType, tc.counted)
			}
		})
	}
}

// The four counts a producer fills have to arrive intact, and cache writes have
// exactly one honest home.
//
// MaxUsage reads cacheWrite as CacheCreationTokens + CacheWriteTokens, so a
// producer that helpfully sets both — they are near-synonyms, and the field
// names invite it — doubles its own cache-write figure. Setting one and leaving
// the other zero is the contract; this pins the sum so the next producer that
// fills both fails here instead of in a monthly cost report.
func TestOutboundStatsAreCountedOnce(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)

	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	writeUsageEntry(t, store, "entry-1", models.TypeOutbound, at)

	usage, err := store.MaxUsage(at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil {
		t.Fatalf("MaxUsage: %v", err)
	}

	byOrchestrator, ok := usage.ByOrchestrator["inber"]
	if !ok {
		t.Fatalf("no usage recorded for orchestrator inber, got %v", usage.ByOrchestrator)
	}
	if byOrchestrator.InputTokens != 100 {
		t.Errorf("InputTokens = %d, want 100", byOrchestrator.InputTokens)
	}
	if byOrchestrator.OutputTokens != 20 {
		t.Errorf("OutputTokens = %d, want 20", byOrchestrator.OutputTokens)
	}
	if byOrchestrator.CacheReadTokens != 9000 {
		t.Errorf("CacheReadTokens = %d, want 9000", byOrchestrator.CacheReadTokens)
	}
	if byOrchestrator.CacheWriteTokens != 500 {
		t.Errorf("CacheWriteTokens = %d, want 500 (CacheCreationTokens + CacheWriteTokens, so only one may be set)", byOrchestrator.CacheWriteTokens)
	}
}

// A string Content with no Stats block is dropped, not counted as zero.
//
// This is the state inber was in: Content is the assistant's reply as a plain
// string, so the legacy fallback marshals it to a JSON string, fails to
// unmarshal that into an object, and hits `continue`. Every token, every dollar
// and the API call itself go with it. Pinned because "we still have the
// deprecated TokensIn/TokensOut fields" reads like a safety net and is not one
// — no usage reader looks at them.
func TestStringContentWithoutStatsIsDropped(t *testing.T) {
	at := time.Now().UTC().Add(-time.Hour)

	store, err := NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	entry := &models.LogEntry{
		ID:           "entry-1",
		Timestamp:    at,
		Orchestrator: "inber",
		Agent:        "claxon",
		Model:        "claude-sonnet-4-5",
		Level:        models.LevelInfo,
		Type:         models.TypeOutbound,
		Content:      "the assistant's reply, as a plain string",
		TokensIn:     100,
		TokensOut:    20,
	}
	if err := store.Write(entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	usage, err := store.MaxUsage(at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil {
		t.Fatalf("MaxUsage: %v", err)
	}
	if usage.Totals.APICalls != 0 {
		t.Fatalf("APICalls = %d, want 0: the deprecated TokensIn/TokensOut fields are not a fallback", usage.Totals.APICalls)
	}
}
