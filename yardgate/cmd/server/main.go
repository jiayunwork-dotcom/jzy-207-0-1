// Command server runs the yard stress-gate service.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"time"

	"yardgate/internal/admission"
	"yardgate/internal/config"
	"yardgate/internal/events"
	"yardgate/internal/httpapi"
	"yardgate/internal/stress"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to the configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	// The database URL may be overridden by the environment (containers).
	if url := os.Getenv("DATABASE_URL"); url != "" {
		cfg.Database.URL = url
	}
	y, err := cfg.BuildYard()
	if err != nil {
		log.Fatalf("yard: %v", err)
	}
	g, err := cfg.BuildGrid()
	if err != nil {
		log.Fatalf("grid: %v", err)
	}

	// Connect to PostgreSQL with retries (the container may still be
	// starting when this service boots).
	var store *events.PGStore
	for attempt := 0; ; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		store, err = events.NewPGStore(ctx, cfg.Database.URL)
		cancel()
		if err == nil {
			break
		}
		if attempt >= 30 {
			log.Fatalf("database: %v", err)
		}
		log.Printf("database not ready (%v), retrying...", err)
		time.Sleep(2 * time.Second)
	}
	defer store.Close()

	disc := stress.Discretizer{
		MaxCellSize: cfg.Discretization.MaxCellSizeM,
		FarFactor:   cfg.Discretization.FarFieldFactor,
	}
	eng, err := admission.Bootstrap(y, g, disc, cfg.Discretization.CutoffKPa, store,
		cfg.Snapshot.IntervalEvents, cfg.Limits.TopViolations)
	if err != nil {
		log.Fatalf("recover state: %v", err)
	}
	defer eng.Close()
	log.Printf("recovered state at event seq %d", eng.Seq())

	router := httpapi.NewRouter(eng)
	log.Printf("listening on %s", cfg.Server.Addr)
	if err := router.Run(cfg.Server.Addr); err != nil {
		log.Fatalf("http: %v", err)
	}
}
