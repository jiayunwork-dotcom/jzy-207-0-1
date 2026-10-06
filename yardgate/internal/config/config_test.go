package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const validYAML = `
ground:
  clay_top_depth_m: 12
checkpoints:
  origin_x: 0
  origin_y: 0
  spacing_x: 5
  spacing_y: 5
  count_x: 10
  count_y: 8
allowance:
  default_kpa: 40
  zones:
    - name: soft
      min_x: 0
      min_y: 0
      max_x: 20
      max_y: 15
      allowance_kpa: 25
yard:
  blocks:
    - id: A
      origin_x: 0
      origin_y: 0
      bays: 2
      rows: 2
      bay_width: 6
      row_width: 2.5
      max_tiers: 5
`

func TestLoadValid(t *testing.T) {
	c, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.Addr != ":8080" {
		t.Errorf("default addr = %q", c.Server.Addr)
	}
	if c.Discretization.MaxCellSizeM != 1.0 || c.Discretization.FarFieldFactor != 4 {
		t.Errorf("discretization defaults not applied: %+v", c.Discretization)
	}
	g, err := c.BuildGrid()
	if err != nil {
		t.Fatal(err)
	}
	if g.Len() != 80 {
		t.Fatalf("grid points = %d", g.Len())
	}
	if a := g.AllowanceAt(g.Index(0, 0)); a != 25 {
		t.Errorf("zone allowance = %v, want 25", a)
	}
	if a := g.AllowanceAt(g.Index(9, 7)); a != 40 {
		t.Errorf("default allowance = %v, want 40", a)
	}
}

func TestRejectInvalidConfigs(t *testing.T) {
	cases := map[string]string{
		"non-positive checkpoint depth": `
ground: {clay_top_depth_m: 0}
checkpoints: {spacing_x: 5, spacing_y: 5, count_x: 4, count_y: 4}
allowance: {default_kpa: 40}
yard: {blocks: [{id: A, bays: 1, rows: 1, bay_width: 6, row_width: 2.5, max_tiers: 5}]}
`,
		"negative depth": `
ground: {clay_top_depth_m: -3}
checkpoints: {spacing_x: 5, spacing_y: 5, count_x: 4, count_y: 4}
allowance: {default_kpa: 40}
yard: {blocks: [{id: A, bays: 1, rows: 1, bay_width: 6, row_width: 2.5, max_tiers: 5}]}
`,
		"non-positive default allowance": `
ground: {clay_top_depth_m: 12}
checkpoints: {spacing_x: 5, spacing_y: 5, count_x: 4, count_y: 4}
allowance: {default_kpa: 0}
yard: {blocks: [{id: A, bays: 1, rows: 1, bay_width: 6, row_width: 2.5, max_tiers: 5}]}
`,
		"non-positive zone allowance": `
ground: {clay_top_depth_m: 12}
checkpoints: {spacing_x: 5, spacing_y: 5, count_x: 4, count_y: 4}
allowance:
  default_kpa: 40
  zones: [{name: bad, min_x: 0, min_y: 0, max_x: 1, max_y: 1, allowance_kpa: -2}]
yard: {blocks: [{id: A, bays: 1, rows: 1, bay_width: 6, row_width: 2.5, max_tiers: 5}]}
`,
		"non-positive slot size": `
ground: {clay_top_depth_m: 12}
checkpoints: {spacing_x: 5, spacing_y: 5, count_x: 4, count_y: 4}
allowance: {default_kpa: 40}
yard: {blocks: [{id: A, bays: 1, rows: 1, bay_width: 0, row_width: 2.5, max_tiers: 5}]}
`,
		"overlapping blocks": `
ground: {clay_top_depth_m: 12}
checkpoints: {spacing_x: 5, spacing_y: 5, count_x: 4, count_y: 4}
allowance: {default_kpa: 40}
yard:
  blocks:
    - {id: A, origin_x: 0, origin_y: 0, bays: 2, rows: 1, bay_width: 6, row_width: 2.5, max_tiers: 5}
    - {id: B, origin_x: 6, origin_y: 0, bays: 2, rows: 1, bay_width: 6, row_width: 2.5, max_tiers: 5}
`,
	}
	for name, body := range cases {
		if _, err := Load(writeTemp(t, body)); err == nil {
			t.Errorf("%s: should be rejected", name)
		}
	}
}
