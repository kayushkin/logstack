package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kayushkin/logstack/internal/store"
	"github.com/kayushkin/logstack/models"
)

// These are the first tests in internal/api. They exist because the guard on
// GET /api/v1/logs/group/:field was reachable by nothing that could fail.
//
// store.IsGroupField is correct and its own package pins it: GroupFieldsAreHandled
// checks every entry in store.GroupFields has a case in getGroupKey. What nothing
// checked is that the handler still ASKS. Deleting the call left the whole suite
// green, and the consequence is not an error — getGroupKey returns "" for a field
// it does not know, so every log collapses into one empty bucket and the endpoint
// answers 200 with a confident, wrong aggregation.
//
// Every request below goes through SetupRoutes rather than calling GroupLogs
// directly. Testing the method alone would leave the route registration unheld,
// which is the same defect one level up.

// recordingStore is a store.Store that records the Group call and returns a fixed
// answer. It deliberately implements nothing else usefully: any handler under test
// here that reaches another method is out of scope and should say so loudly.
type recordingStore struct {
	groupCalls  []string
	groupParams []models.QueryParams
}

func (s *recordingStore) Group(params models.QueryParams, groupBy string) ([]models.GroupedLogs, error) {
	s.groupCalls = append(s.groupCalls, groupBy)
	s.groupParams = append(s.groupParams, params)
	return []models.GroupedLogs{{GroupKey: "some-bucket", Count: 1}}, nil
}

func (s *recordingStore) Write(*models.LogEntry) error { return nil }
func (s *recordingStore) Query(models.QueryParams) ([]models.LogEntry, error) {
	return nil, nil
}
func (s *recordingStore) Stats(models.QueryParams) (*models.Stats, error) { return &models.Stats{}, nil }
func (s *recordingStore) Usage(time.Time) ([]models.UsageStats, error)    { return nil, nil }
func (s *recordingStore) MaxUsage(time.Time, time.Time) (*models.MaxUsageResponse, error) {
	return &models.MaxUsageResponse{}, nil
}
func (s *recordingStore) RateLimits(time.Time, int) (*models.RateLimitsResponse, error) {
	return &models.RateLimitsResponse{}, nil
}
func (s *recordingStore) Get(string) (*models.LogEntry, error)   { return &models.LogEntry{}, nil }
func (s *recordingStore) Delete(models.QueryParams) (int, error) { return 0, nil }

// groupRequest issues GET /api/v1/logs/group/<field> through the real route table.
func groupRequest(t *testing.T, field string) (*httptest.ResponseRecorder, *recordingStore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := &recordingStore{}
	r := gin.New()
	NewHandler(s).SetupRoutes(r)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/logs/group/"+field, nil))
	return w, s
}

// The load-bearing assertion is "the store was never asked", not the status code.
// This handler answers 400 for two unrelated reasons — an unknown group field and
// a query string it cannot bind — so a test that checked only the status could not
// tell the guard firing from the binder failing, and would stay green with the
// guard deleted as long as something else refused the request.
func TestAnUnknownGroupFieldNeverReachesTheStore(t *testing.T) {
	for _, field := range []string{"nonsense", "orchestratorr", "ORCHESTRATOR", "id", "count"} {
		t.Run(field, func(t *testing.T) {
			if store.IsGroupField(field) {
				t.Fatalf("fixture error: %q is a real group field, so it cannot test the refusal", field)
			}
			w, s := groupRequest(t, field)

			if len(s.groupCalls) != 0 {
				t.Errorf("the store was asked to group by %q: %v — the guard did not stop it",
					field, s.groupCalls)
			}
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want %d for unknown group field %q", w.Code, http.StatusBadRequest, field)
			}

			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
			}
			if body["error"] != "invalid group field" {
				t.Errorf("error = %q, want %q — this is what separates the guard's 400 from the binder's",
					body["error"], "invalid group field")
			}
		})
	}
}

// The other direction. A guard that refuses everything also keeps the store
// untouched, and the test above would not notice; this one fails if the guard
// stops letting real fields through.
func TestEveryFieldTheStoreDeclaresIsAcceptedAndPassedThrough(t *testing.T) {
	// Cry-wolf control. The loop below ranges over store.GroupFields, so an empty
	// or truncated list would make it vacuously green — the fixture would shrink in
	// step with the thing it measures. The store's own GroupFieldsAreHandled pins
	// what is IN the list; this only refuses to be silent if the list disappears.
	if len(store.GroupFields) < 2 {
		t.Fatalf("store.GroupFields has %d entries: this test cannot say anything about a guard "+
			"with nothing to admit", len(store.GroupFields))
	}

	for _, field := range store.GroupFields {
		t.Run(field, func(t *testing.T) {
			w, s := groupRequest(t, field)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200: the guard refused %q, which store.GroupFields declares valid (%s)",
					w.Code, field, w.Body.String())
			}
			if len(s.groupCalls) != 1 {
				t.Fatalf("store.Group called %d times for %q, want 1", len(s.groupCalls), field)
			}
			if s.groupCalls[0] != field {
				t.Errorf("store.Group asked for %q, want %q — the handler grouped by something "+
					"other than the field in the path", s.groupCalls[0], field)
			}

			var body struct {
				GroupBy string `json:"group_by"`
				Count   int    `json:"count"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
			}
			if body.GroupBy != field {
				t.Errorf("group_by = %q, want %q", body.GroupBy, field)
			}
		})
	}
}

// The refusal advertises the fields that would have worked. That list must be the
// store's, not a copy: a second authoring in the handler would go stale the day a
// field is added to store.GroupFields, and the endpoint would then refuse a field
// it actually supports while naming the wrong set in the same breath.
func TestTheRefusalNamesTheStoresOwnFieldList(t *testing.T) {
	w, _ := groupRequest(t, "nonsense")

	var body struct {
		Valid []string `json:"valid"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %v (%s)", err, w.Body.String())
	}
	if !reflect.DeepEqual(body.Valid, store.GroupFields) {
		t.Errorf("valid = %v, want %v (store.GroupFields, in order)", body.Valid, store.GroupFields)
	}
}
