// Command server runs the yard geotechnical admission service.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"yardstress/internal/admit"
	"yardstress/internal/config"
	"yardstress/internal/discretize"
	"yardstress/internal/geom"
	"yardstress/internal/grid"
	"yardstress/internal/httpapi"
	"yardstress/internal/store"
	"yardstress/internal/yard"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := os.Getenv("CONFIG_PATH")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}
	cfg, ferrs, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	if len(ferrs) > 0 {
		return fmt.Errorf("invalid configuration:\n  %s", ferrs)
	}

	y, yerrs := yard.New(cfg.Blocks)
	if len(yerrs) > 0 {
		return fmt.Errorf("invalid yard layout:\n  %s", yerrs)
	}

	g := grid.New(
		cfg.Grid.Origin[0], cfg.Grid.Origin[1],
		cfg.Grid.Spacing[0], cfg.Grid.Spacing[1],
		cfg.Grid.Count[0], cfg.Grid.Count[1],
		cfg.Ground.ClayTopDepth,
		nil, cfg.Engine.TileSize,
	)
	var zones []grid.Zone
	for _, z := range cfg.Allowable.Zones {
		zones = append(zones, grid.Zone{
			Rect:  geom.Rect{MinX: z.Rect[0], MinY: z.Rect[1], MaxX: z.Rect[2], MaxY: z.Rect[3]},
			Value: z.Value,
		})
	}
	g.Allow = grid.Allowables(g, cfg.Allowable.Default, zones)

	var st store.Store
	if cfg.Database.DSN != "" {
		var pg *store.Postgres
		for attempt := 1; ; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			pg, err = store.NewPostgres(ctx, cfg.Database.DSN)
			cancel()
			if err == nil {
				break
			}
			if attempt >= 30 {
				return fmt.Errorf("cannot reach database: %w", err)
			}
			fmt.Printf("waiting for database (%v)\n", err)
			time.Sleep(time.Second)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = pg.Migrate(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		st = pg
	} else {
		fmt.Println("DATABASE_URL/database.dsn not set: using in-memory store (data is lost on restart)")
		st = store.NewMemory()
	}
	defer st.Close()

	eng, err := admit.New(y, g, discretize.Params{
		MaxCell: cfg.Discretization.MaxCell,
		Cutoff:  cfg.Discretization.Cutoff,
	}, st, admit.Params{
		TopK:             cfg.Engine.TopK,
		SnapshotInterval: cfg.Engine.SnapshotInterval,
	})
	if err != nil {
		return fmt.Errorf("recover state: %w", err)
	}

	addr := cfg.Server.Addr
	if addr == "" {
		addr = ":8080"
	}
	fmt.Printf("yardstress listening on %s (recovered to event seq %d)\n", addr, eng.Seq())
	return httpapi.NewRouter(eng).Run(addr)
}
