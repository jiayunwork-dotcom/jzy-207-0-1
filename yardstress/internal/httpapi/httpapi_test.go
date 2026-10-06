package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"yardstress/internal/admit"
	"yardstress/internal/discretize"
	"yardstress/internal/grid"
	"yardstress/internal/store"
	"yardstress/internal/yard"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	y, errs := yard.New([]yard.BlockCfg{
		{ID: "A", OriginX: 10, OriginY: 8, Bays: 1, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 3},
		{ID: "B", OriginX: 22.5, OriginY: 8, Bays: 1, Rows: 1, BayWidth: 12.5, RowWidth: 3, MaxTiers: 3},
	})
	if len(errs) > 0 {
		t.Fatalf("yard: %v", errs)
	}
	g := grid.New(0, 0, 2, 2, 30, 12, 12, nil, 4)
	allow := make([]float64, g.Len())
	for i := range allow {
		allow[i] = 20 // deliberately tight, so heavy jobs get rejected
	}
	g.Allow = allow
	eng, err := admit.New(y, g, discretize.Params{MaxCell: 3, Cutoff: 1e-6},
		store.NewMemory(), admit.Params{TopK: 5, SnapshotInterval: 2})
	if err != nil {
		t.Fatal(err)
	}
	return httptest.NewServer(NewRouter(eng))
}

func post(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, out
}

func get(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, out
}

func TestJobLifecycleOverHTTP(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	// Validation failure: weight not positive -> 422 naming the field.
	code, body := post(t, srv.URL+"/api/v1/jobs", map[string]any{
		"type": "PLACE", "stack_id": "A-B01-R01", "weight": -3, "tiers": 1,
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %v", code, body)
	}
	errs := body["errors"].([]any)
	if errs[0].(map[string]any)["field"] != "weight" {
		t.Fatalf("expected weight field error: %v", body)
	}

	// Geotechnical rejection: 10 000 kN under a 20 kPa allowable -> 409.
	code, body = post(t, srv.URL+"/api/v1/jobs", map[string]any{
		"type": "PLACE", "stack_id": "A-B01-R01", "weight": 10000, "tiers": 1,
	})
	if code != http.StatusConflict {
		t.Fatalf("got %d, want 409: %v", code, body)
	}
	if body["reason"] != "allowable_exceeded" {
		t.Fatalf("unexpected body: %v", body)
	}
	if len(body["exceedances"].([]any)) == 0 {
		t.Fatal("409 must carry exceedances")
	}
	ex := body["exceedances"].([]any)[0].(map[string]any)
	for _, k := range []string{"x", "y", "stress", "allowable", "excess"} {
		if _, ok := ex[k]; !ok {
			t.Fatalf("exceedance missing %q: %v", k, ex)
		}
	}

	// Admissible job -> 201 with seq 1.
	code, body = post(t, srv.URL+"/api/v1/jobs", map[string]any{
		"type": "PLACE", "stack_id": "A-B01-R01", "weight": 500, "tiers": 1,
	})
	if code != http.StatusCreated || body["seq"].(float64) != 1 {
		t.Fatalf("got %d %v, want 201 seq=1", code, body)
	}

	// Historical query.
	code, body = get(t, srv.URL+"/api/v1/grid/at/1")
	if code != http.StatusOK || body["seq"].(float64) != 1 {
		t.Fatalf("grid/at/1: got %d %v", code, body)
	}
	vals := body["values"].([]any)
	if len(vals) != 30*12 {
		t.Fatalf("grid has %d values, want %d", len(vals), 30*12)
	}
	// Beyond the log -> 404.
	if code, _ = get(t, srv.URL+"/api/v1/grid/at/99"); code != http.StatusNotFound {
		t.Fatalf("grid/at/99: got %d, want 404", code)
	}

	// Current grid equals the state at the latest event.
	code, cur := get(t, srv.URL+"/api/v1/grid/current")
	if code != http.StatusOK || cur["seq"].(float64) != 1 {
		t.Fatalf("current: %v", cur)
	}

	// Events listing.
	code, body = get(t, srv.URL+"/api/v1/events")
	if code != http.StatusOK || len(body["events"].([]any)) != 1 {
		t.Fatalf("events: %v", body)
	}

	// Stacks listing.
	code, body = get(t, srv.URL+"/api/v1/stacks")
	stacks := body["stacks"].([]any)
	if code != http.StatusOK || len(stacks) != 2 {
		t.Fatalf("stacks: %v", body)
	}
	first := stacks[0].(map[string]any)
	if first["id"] != "A-B01-R01" || first["weight"].(float64) != 500 || first["tiers"].(float64) != 1 {
		t.Fatalf("stack state wrong: %v", first)
	}
}

func TestCorrectionOverHTTP(t *testing.T) {
	srv := testServer(t)
	defer srv.Close()

	post(t, srv.URL+"/api/v1/jobs", map[string]any{
		"type": "PLACE", "stack_id": "A-B01-R01", "weight": 500, "tiers": 1,
	})
	// Bad correction: non-positive weight -> 422.
	code, body := post(t, srv.URL+"/api/v1/corrections", map[string]any{
		"event_id": 1, "new_weight": 0,
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422: %v", code, body)
	}
	// Unknown event -> 422.
	code, _ = post(t, srv.URL+"/api/v1/corrections", map[string]any{
		"event_id": 42, "new_weight": 100,
	})
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("got %d, want 422", code)
	}
	// Valid correction -> 200 with the new event seq and the impact list.
	code, body = post(t, srv.URL+"/api/v1/corrections", map[string]any{
		"event_id": 1, "new_weight": 800,
	})
	if code != http.StatusOK || body["seq"].(float64) != 2 {
		t.Fatalf("got %d %v, want 200 seq=2", code, body)
	}
	if _, ok := body["impacted"]; !ok {
		t.Fatalf("response missing impacted: %v", body)
	}
	// The log now holds 2 events; history at seq 1 is unchanged.
	_, body = get(t, srv.URL+"/api/v1/events")
	if len(body["events"].([]any)) != 2 {
		t.Fatalf("events: %v", body)
	}
}
