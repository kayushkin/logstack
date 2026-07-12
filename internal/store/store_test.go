package store

import (
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
