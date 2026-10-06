package httpapi_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"yardgate/internal/admission"
	"yardgate/internal/events"
	"yardgate/internal/grid"
	"yardgate/internal/httpapi"
	"yardgate/internal/stress"
	"yardgate/internal/yard"
)

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	y, err := yard.Build([]yard.Block{
		{ID: "A", OriginX: -1, OriginY: 0, Bays: 1, Rows: 1, BayWidth: 1, RowWidth: 1, MaxTiers: 5},
		{ID: "B", OriginX: 0, OriginY: 0, Bays: 1, Rows: 1, BayWidth: 1, RowWidth: 1, MaxTiers: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	g, err := grid.New(-4, -2, 1, 1, 9, 5, 2, nil, 12)
	if err != nil {
		t.Fatal(err)
	}
	eng := admission.NewEngine(y, g, stress.Discretizer{MaxCellSize: 0.5, FarFactor: 4}, 1e-6, events.NewMemStore(), 0, 5)
	t.Cleanup(eng.Close)
	srv := httptest.NewServer(httpapi.NewRouter(eng))
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	return resp.StatusCode, out
}

func TestJobLifecycleOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	// Health.
	code, body := get(t, srv.URL+"/api/health")
	if code != 200 || body["status"] != "ok" {
		t.Fatalf("health: %d %v", code, body)
	}

	// Place a box: accepted, seq 1.
	code, body = post(t, srv.URL+"/api/jobs", map[string]any{
		"type": "place", "block": "A", "bay": 0, "row": 0, "weight_kn": 100, "tiers": 1,
	})
	if code != 200 || body["accepted"] != true || body["seq"] != 1.0 {
		t.Fatalf("place: %d %v", code, body)
	}

	// A second box on the adjacent slot exceeds the allowance: 409 with violations.
	code, body = post(t, srv.URL+"/api/jobs", map[string]any{
		"type": "place", "block": "B", "bay": 0, "row": 0, "weight_kn": 100, "tiers": 1,
	})
	if code != http.StatusConflict || body["accepted"] != false {
		t.Fatalf("over-limit place: %d %v", code, body)
	}
	viol, ok := body["violations"].([]any)
	if !ok || len(viol) == 0 {
		t.Fatalf("violations missing: %v", body)
	}
	v0 := viol[0].(map[string]any)
	if v0["excess_kpa"].(float64) <= 0 {
		t.Fatalf("excess must be positive: %v", v0)
	}

	// Invalid weight: 400 naming the field.
	code, body = post(t, srv.URL+"/api/jobs", map[string]any{
		"type": "place", "block": "A", "bay": 0, "row": 0, "weight_kn": -3, "tiers": 1,
	})
	if code != http.StatusBadRequest || body["field"] != "weight_kn" {
		t.Fatalf("bad weight: %d %v", code, body)
	}

	// Too many tiers: 400.
	code, body = post(t, srv.URL+"/api/jobs", map[string]any{
		"type": "place", "block": "A", "bay": 0, "row": 0, "weight_kn": 100, "tiers": 99,
	})
	if code != http.StatusBadRequest || body["field"] != "tiers" {
		t.Fatalf("too many tiers: %d %v", code, body)
	}

	// Remove more tiers than present: 400.
	code, body = post(t, srv.URL+"/api/jobs", map[string]any{
		"type": "remove", "block": "B", "bay": 0, "row": 0, "tiers": 1,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("remove from empty: %d %v", code, body)
	}

	// Grid queries.
	code, cur := get(t, srv.URL+"/api/grid/current")
	if code != 200 || cur["seq"] != 1.0 {
		t.Fatalf("grid current: %d %v", code, cur["seq"])
	}
	vals := cur["values"].([]any)
	if len(vals) != 45 {
		t.Fatalf("grid values = %d, want 45", len(vals))
	}
	code, at0 := get(t, srv.URL+"/api/grid/at/0")
	if code != 200 {
		t.Fatalf("grid at 0: %d", code)
	}
	for _, v := range at0["values"].([]any) {
		if v.(float64) != 0 {
			t.Fatal("grid at seq 0 must be empty")
		}
	}
	code, at1 := get(t, srv.URL+"/api/grid/at/1")
	if code != 200 {
		t.Fatalf("grid at 1: %d", code)
	}
	// Current grid must equal the grid at the latest seq.
	for i, v := range at1["values"].([]any) {
		if v.(float64) != vals[i].(float64) {
			t.Fatalf("grid at 1 differs from current at %d", i)
		}
	}
	code, _ = get(t, srv.URL+"/api/grid/at/99")
	if code != http.StatusBadRequest {
		t.Fatalf("grid at 99 should be 400")
	}

	// Events list.
	code, body = get(t, srv.URL+"/api/events")
	if code != 200 || body["count"] != 1.0 {
		t.Fatalf("events: %d %v", code, body)
	}
}

func TestCorrectionOverHTTP(t *testing.T) {
	srv := newTestServer(t)

	mustPlace := func(block string, w float64) {
		t.Helper()
		code, body := post(t, srv.URL+"/api/jobs", map[string]any{
			"type": "place", "block": block, "bay": 0, "row": 0, "weight_kn": w, "tiers": 1,
		})
		if code != 200 {
			t.Fatalf("place %s: %d %v", block, code, body)
		}
	}
	// allowance 12: 50 kN on A (~4.3) + 100 kN on B (~8.5) = 12.8 would
	// exceed... use lighter second box so both fit, then correct upwards.
	mustPlace("A", 50)
	code, body := post(t, srv.URL+"/api/jobs", map[string]any{
		"type": "place", "block": "B", "bay": 0, "row": 0, "weight_kn": 80, "tiers": 1,
	})
	if code != 200 {
		t.Fatalf("place B: %d %v", code, body)
	}
	// Remove A's box so the correction does not break the current grid.
	code, body = post(t, srv.URL+"/api/jobs", map[string]any{
		"type": "remove", "block": "A", "bay": 0, "row": 0, "tiers": 1,
	})
	if code != 200 {
		t.Fatalf("remove: %d %v", code, body)
	}
	// Correct event 1 from 50 to 100 kN: accepted, job 2 is flagged.
	code, body = post(t, srv.URL+"/api/corrections", map[string]any{
		"target_seq": 1, "new_weight_kn": 100,
	})
	if code != 200 || body["accepted"] != true {
		t.Fatalf("correction: %d %v", code, body)
	}
	affected, _ := body["affected_seqs"].([]any)
	if len(affected) != 1 || affected[0].(float64) != 2 {
		t.Fatalf("affected = %v, want [2]", body["affected_seqs"])
	}
	// Bad correction weight: 400.
	code, body = post(t, srv.URL+"/api/corrections", map[string]any{
		"target_seq": 1, "new_weight_kn": 0,
	})
	if code != http.StatusBadRequest || body["field"] != "new_weight_kn" {
		t.Fatalf("bad correction: %d %v", code, body)
	}
}
