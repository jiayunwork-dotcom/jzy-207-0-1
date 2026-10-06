package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validYAML = `
server:
  addr: ":8080"
ground:
  clay_top_depth: 12
grid:
  origin: [0, 0]
  spacing: [2, 2]
  count: [50, 30]
allowable:
  default: 60
  zones:
    - rect: [0, 0, 40, 20]
      value: 45
discretization:
  max_cell: 3
  cutoff: 1.0e-6
engine:
  tile_size: 8
  snapshot_interval: 1000
  top_k: 5
blocks:
  - id: A
    origin_x: 10
    origin_y: 10
    bays: 4
    rows: 3
    bay_width: 12.5
    row_width: 3
    max_tiers: 5
`

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValid(t *testing.T) {
	cfg, errs, err := Load(writeTemp(t, validYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected validation errors: %v", errs)
	}
	if cfg.Ground.ClayTopDepth != 12 || cfg.Grid.Count[0] != 50 || len(cfg.Blocks) != 1 {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

// Each invalid field must be rejected with its field path named.
func TestValidateFieldErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *Config)
		field  string
	}{
		{"checkpoint depth not positive", func(c *Config) { c.Ground.ClayTopDepth = 0 }, "ground.clay_top_depth"},
		{"allowable default not positive", func(c *Config) { c.Allowable.Default = -5 }, "allowable.default"},
		{"allowable zone not positive", func(c *Config) { c.Allowable.Zones[0].Value = 0 }, "allowable.zones[0].value"},
		{"grid spacing not positive", func(c *Config) { c.Grid.Spacing[0] = 0 }, "grid.spacing[0]"},
		{"grid count not positive", func(c *Config) { c.Grid.Count[1] = 0 }, "grid.count[1]"},
		{"max cell not positive", func(c *Config) { c.Discretization.MaxCell = 0 }, "discretization.max_cell"},
		{"cutoff not positive", func(c *Config) { c.Discretization.Cutoff = 0 }, "discretization.cutoff"},
		{"no blocks", func(c *Config) { c.Blocks = nil }, "blocks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, errs, err := Load(writeTemp(t, validYAML))
			if err != nil || len(errs) > 0 {
				t.Fatalf("baseline config invalid: %v %v", err, errs)
			}
			tc.mutate(cfg)
			errs = cfg.Validate()
			if !errs.Has(tc.field) {
				t.Errorf("expected error for field %q, got: %v", tc.field, errs)
			}
		})
	}
}

// Invalid YAML in the file must surface field-named errors after loading.
func TestLoadInvalidFile(t *testing.T) {
	bad := strings.Replace(validYAML, "clay_top_depth: 12", "clay_top_depth: -1", 1)
	_, errs, err := Load(writeTemp(t, bad))
	if err != nil {
		t.Fatal(err)
	}
	if !errs.Has("ground.clay_top_depth") {
		t.Fatalf("expected ground.clay_top_depth error, got: %v", errs)
	}
}
