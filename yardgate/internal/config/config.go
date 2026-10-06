// Package config loads and validates the service configuration: yard
// layout, checkpoint mesh, allowance zones, discretization and storage.
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"

	"yardgate/internal/grid"
	"yardgate/internal/yard"
)

// Config is the root configuration.
type Config struct {
	Server struct {
		Addr string `yaml:"addr"`
	} `yaml:"server"`
	Database struct {
		URL string `yaml:"url"`
	} `yaml:"database"`
	Ground struct {
		// ClayTopDepthM is the depth of the clay layer top surface below
		// the yard surface; it is the checkpoint depth and must be > 0.
		ClayTopDepthM float64 `yaml:"clay_top_depth_m"`
	} `yaml:"ground"`
	Discretization struct {
		MaxCellSizeM   float64 `yaml:"max_cell_size_m"`
		FarFieldFactor float64 `yaml:"far_field_factor"`
		CutoffKPa      float64 `yaml:"cutoff_kpa"`
	} `yaml:"discretization"`
	Checkpoints struct {
		OriginX  float64 `yaml:"origin_x"`
		OriginY  float64 `yaml:"origin_y"`
		SpacingX float64 `yaml:"spacing_x"`
		SpacingY float64 `yaml:"spacing_y"`
		CountX   int     `yaml:"count_x"`
		CountY   int     `yaml:"count_y"`
	} `yaml:"checkpoints"`
	Allowance struct {
		DefaultKPa float64 `yaml:"default_kpa"`
		Zones      []struct {
			Name         string  `yaml:"name"`
			MinX         float64 `yaml:"min_x"`
			MinY         float64 `yaml:"min_y"`
			MaxX         float64 `yaml:"max_x"`
			MaxY         float64 `yaml:"max_y"`
			AllowanceKPa float64 `yaml:"allowance_kpa"`
		} `yaml:"zones"`
	} `yaml:"allowance"`
	Yard struct {
		Blocks []yard.Block `yaml:"blocks"`
	} `yaml:"yard"`
	Snapshot struct {
		IntervalEvents int64 `yaml:"interval_events"`
	} `yaml:"snapshot"`
	Limits struct {
		TopViolations int `yaml:"top_violations"`
	} `yaml:"limits"`
}

// Load reads and validates the configuration file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.setDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) setDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Discretization.MaxCellSizeM == 0 {
		c.Discretization.MaxCellSizeM = 1.0
	}
	if c.Discretization.FarFieldFactor == 0 {
		c.Discretization.FarFieldFactor = 4
	}
	if c.Discretization.CutoffKPa == 0 {
		c.Discretization.CutoffKPa = 1e-6
	}
	if c.Snapshot.IntervalEvents == 0 {
		c.Snapshot.IntervalEvents = 1000
	}
	if c.Limits.TopViolations == 0 {
		c.Limits.TopViolations = 5
	}
}

// Validate rejects invalid configuration, including non-positive checkpoint
// depth, non-positive allowances and bad slot geometry.
func (c *Config) Validate() error {
	if c.Ground.ClayTopDepthM <= 0 {
		return fmt.Errorf("ground.clay_top_depth_m must be positive, got %v", c.Ground.ClayTopDepthM)
	}
	if c.Discretization.MaxCellSizeM <= 0 {
		return fmt.Errorf("discretization.max_cell_size_m must be positive")
	}
	if c.Discretization.FarFieldFactor < 1 {
		return fmt.Errorf("discretization.far_field_factor must be >= 1")
	}
	if c.Discretization.CutoffKPa <= 0 {
		return fmt.Errorf("discretization.cutoff_kpa must be positive")
	}
	cp := c.Checkpoints
	if cp.SpacingX <= 0 || cp.SpacingY <= 0 || cp.CountX <= 0 || cp.CountY <= 0 {
		return fmt.Errorf("checkpoints: spacing and counts must be positive")
	}
	if c.Allowance.DefaultKPa <= 0 {
		return fmt.Errorf("allowance.default_kpa must be positive, got %v", c.Allowance.DefaultKPa)
	}
	for _, z := range c.Allowance.Zones {
		if z.AllowanceKPa <= 0 {
			return fmt.Errorf("allowance zone %q: allowance_kpa must be positive, got %v", z.Name, z.AllowanceKPa)
		}
	}
	if _, err := c.BuildYard(); err != nil {
		return err
	}
	if _, err := c.BuildGrid(); err != nil {
		return err
	}
	return nil
}

// BuildYard constructs the validated yard model.
func (c *Config) BuildYard() (*yard.Yard, error) {
	return yard.Build(c.Yard.Blocks)
}

// BuildGrid constructs the checkpoint grid with resolved allowances.
func (c *Config) BuildGrid() (*grid.Grid, error) {
	zones := make([]grid.Zone, 0, len(c.Allowance.Zones))
	for _, z := range c.Allowance.Zones {
		zones = append(zones, grid.Zone{
			Name: z.Name, MinX: z.MinX, MinY: z.MinY, MaxX: z.MaxX, MaxY: z.MaxY,
			AllowanceKPa: z.AllowanceKPa,
		})
	}
	cp := c.Checkpoints
	return grid.New(cp.OriginX, cp.OriginY, cp.SpacingX, cp.SpacingY, cp.CountX, cp.CountY,
		c.Ground.ClayTopDepthM, zones, c.Allowance.DefaultKPa)
}
