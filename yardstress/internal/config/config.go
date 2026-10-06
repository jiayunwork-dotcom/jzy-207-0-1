// Package config loads and validates the service configuration. Every
// violation is reported with its field path; the service refuses to start
// on any error.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"yardstress/internal/ferr"
	"yardstress/internal/yard"
)

// ZoneCfg assigns an allowable stress to a rectangular region.
type ZoneCfg struct {
	Rect  [4]float64 `yaml:"rect"` // minx, miny, maxx, maxy
	Value float64    `yaml:"value"`
}

// Config is the root configuration document.
type Config struct {
	Server struct {
		Addr string `yaml:"addr"`
	} `yaml:"server"`
	Database struct {
		DSN string `yaml:"dsn"`
	} `yaml:"database"`
	Ground struct {
		// ClayTopDepth is the depth (m) of the clay layer top surface below
		// the yard surface; it is the checkpoint depth.
		ClayTopDepth float64 `yaml:"clay_top_depth"`
	} `yaml:"ground"`
	Grid struct {
		Origin  [2]float64 `yaml:"origin"`
		Spacing [2]float64 `yaml:"spacing"`
		Count   [2]int     `yaml:"count"`
	} `yaml:"grid"`
	Allowable struct {
		Default float64   `yaml:"default"`
		Zones   []ZoneCfg `yaml:"zones"`
	} `yaml:"allowable"`
	Discretization struct {
		MaxCell float64 `yaml:"max_cell"`
		Cutoff  float64 `yaml:"cutoff"`
	} `yaml:"discretization"`
	Engine struct {
		TileSize         int   `yaml:"tile_size"`
		SnapshotInterval int64 `yaml:"snapshot_interval"`
		TopK             int   `yaml:"top_k"`
	} `yaml:"engine"`
	Blocks []yard.BlockCfg `yaml:"blocks"`
}

// Load reads and validates the configuration file. The DATABASE_URL
// environment variable overrides database.dsn.
func Load(path string) (*Config, ferr.List, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		cfg.Database.DSN = dsn
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		return nil, errs, nil
	}
	return &cfg, nil, nil
}

// Validate checks every scalar constraint; block-level and cross-stack
// constraints are checked by yard.New.
func (c *Config) Validate() ferr.List {
	var errs ferr.List
	if !(c.Ground.ClayTopDepth > 0) {
		errs = ferr.Appendf(errs, "ground.clay_top_depth", "must be positive, got %v", c.Ground.ClayTopDepth)
	}
	for i, v := range c.Grid.Spacing {
		if !(v > 0) {
			errs = ferr.Appendf(errs, fmt.Sprintf("grid.spacing[%d]", i), "must be positive, got %v", v)
		}
	}
	for i, v := range c.Grid.Count {
		if v < 1 {
			errs = ferr.Appendf(errs, fmt.Sprintf("grid.count[%d]", i), "must be >= 1, got %d", v)
		}
	}
	if !(c.Allowable.Default > 0) {
		errs = ferr.Appendf(errs, "allowable.default", "must be positive, got %v", c.Allowable.Default)
	}
	for i, z := range c.Allowable.Zones {
		if !(z.Value > 0) {
			errs = ferr.Appendf(errs, fmt.Sprintf("allowable.zones[%d].value", i), "must be positive, got %v", z.Value)
		}
		if !(z.Rect[0] < z.Rect[2] && z.Rect[1] < z.Rect[3]) {
			errs = ferr.Appendf(errs, fmt.Sprintf("allowable.zones[%d].rect", i), "must be minx,miny,maxx,maxy with min<max, got %v", z.Rect)
		}
	}
	if !(c.Discretization.MaxCell > 0) {
		errs = ferr.Appendf(errs, "discretization.max_cell", "must be positive, got %v", c.Discretization.MaxCell)
	}
	if !(c.Discretization.Cutoff > 0) {
		errs = ferr.Appendf(errs, "discretization.cutoff", "must be positive, got %v", c.Discretization.Cutoff)
	}
	if c.Engine.TileSize < 1 {
		errs = ferr.Appendf(errs, "engine.tile_size", "must be >= 1, got %d", c.Engine.TileSize)
	}
	if c.Engine.SnapshotInterval < 0 {
		errs = ferr.Appendf(errs, "engine.snapshot_interval", "must be >= 0, got %d", c.Engine.SnapshotInterval)
	}
	if c.Engine.TopK < 1 {
		errs = ferr.Appendf(errs, "engine.top_k", "must be >= 1, got %d", c.Engine.TopK)
	}
	if len(c.Blocks) == 0 {
		errs = ferr.Appendf(errs, "blocks", "at least one block is required")
	}
	return errs
}
