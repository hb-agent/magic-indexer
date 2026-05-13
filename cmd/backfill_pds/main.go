// Command backfill_pds resolves the PDS service endpoint for every actor
// row whose pds column is NULL or empty and writes it back. This is a
// one-shot operational command meant to be run after migrations 019/020
// land so the excludePds GraphQL filter has data to filter against. It
// is safe to run repeatedly (idempotent) — the default scan only touches
// actor rows whose pds is unresolved. Pass --force to re-resolve every
// actor regardless (use sparingly: it bypasses the resolution-failure
// preservation that the steady-state ingestion path relies on).
//
// Usage:
//
//	DATABASE_URL=postgres://… go run ./cmd/backfill_pds
//	DATABASE_URL=postgres://… go run ./cmd/backfill_pds --force
//	DATABASE_URL=postgres://… go run ./cmd/backfill_pds --concurrency=4 --rate=5
//
// The defaults — 8 concurrent workers, 10 req/s globally — sit below
// plc.directory's empirical headroom. Lower them if a backfill against a
// shared resolver host is starting to noise your error budget.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/time/rate"

	"github.com/GainForest/hypergoat/internal/config"
	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/oauth"
	"github.com/GainForest/hypergoat/internal/server"
)

func main() {
	if err := run(); err != nil {
		slog.Error("backfill_pds failed", "error", err)
		os.Exit(1)
	}
}

type flags struct {
	force       bool
	concurrency int
	ratePerSec  float64
	progressN   int
}

func run() error {
	var f flags
	flag.BoolVar(&f.force, "force", false, "re-resolve every actor (bypasses the NULL-pds guard)")
	flag.IntVar(&f.concurrency, "concurrency", 8, "number of resolver workers (bounded by --rate too)")
	flag.Float64Var(&f.ratePerSec, "rate", 10.0, "max resolve calls per second across all workers (rate limiter)")
	flag.IntVar(&f.progressN, "progress-every", 100, "log a progress line every N actors")
	flag.Parse()

	if f.concurrency < 1 {
		f.concurrency = 1
	}
	if f.ratePerSec <= 0 {
		f.ratePerSec = 1
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL env not set")
	}

	db, err := server.ConnectDatabase(cfg.DatabaseURL, cfg.DBStatementTimeoutMs)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}

	actors := repositories.NewActorsRepository(db)
	resolverOpts := []oauth.DIDResolverOption{}
	if cfg.PLCDirectoryURL != "" {
		resolverOpts = append(resolverOpts, oauth.WithPLCDirectoryURL(cfg.PLCDirectoryURL))
	}
	cache := oauth.NewDIDCache(
		oauth.WithCacheTTL(24*time.Hour),
		oauth.WithResolver(oauth.NewDIDResolver(resolverOpts...)),
	)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dids, err := loadDIDsToResolve(ctx, db, f.force)
	if err != nil {
		return fmt.Errorf("load DIDs: %w", err)
	}
	slog.Info("backfill_pds starting",
		"actors_to_resolve", len(dids),
		"concurrency", f.concurrency,
		"rate_per_sec", f.ratePerSec,
		"force", f.force)

	limiter := rate.NewLimiter(rate.Limit(f.ratePerSec), f.concurrency)

	work := make(chan string)
	var wg sync.WaitGroup
	var resolved, failed, noEndpoint int64

	for i := 0; i < f.concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for did := range work {
				if err := limiter.Wait(ctx); err != nil {
					return
				}
				doc, err := cache.Get(did)
				if err != nil {
					slog.Warn("resolve failed", "did", did, "error", err)
					atomic.AddInt64(&failed, 1)
					continue
				}
				pds := doc.GetPDSEndpoint()
				if pds == "" {
					slog.Debug("DID document has no PDS endpoint", "did", did)
					atomic.AddInt64(&noEndpoint, 1)
					continue
				}
				// SetPDS is a pure UPDATE that touches only the pds
				// column: leaves handle and indexed_at alone so the
				// backfill cannot stomp metadata that ingestion may
				// have written in the meantime. If the actor row
				// disappeared between loadDIDsToResolve and now, the
				// UPDATE matches zero rows and we move on.
				if err := actors.SetPDS(ctx, did, pds); err != nil {
					slog.Warn("set pds", "did", did, "error", err)
					atomic.AddInt64(&failed, 1)
					continue
				}
				n := atomic.AddInt64(&resolved, 1)
				if f.progressN > 0 && n%int64(f.progressN) == 0 {
					slog.Info("backfill progress", "resolved", n, "failed", atomic.LoadInt64(&failed), "no_endpoint", atomic.LoadInt64(&noEndpoint))
				}
			}
		}()
	}

dispatch:
	for _, did := range dids {
		select {
		case <-ctx.Done():
			break dispatch
		case work <- did:
		}
	}
	close(work)
	wg.Wait()

	slog.Info("backfill_pds done",
		"resolved", resolved,
		"failed", failed,
		"no_endpoint", noEndpoint,
		"total", len(dids))
	return nil
}

// loadDIDsToResolve queries the actor table and returns the DIDs to
// resolve. The default scan picks up only actors whose pds column is
// NULL or empty; --force returns every actor.
func loadDIDsToResolve(ctx context.Context, db database.Executor, force bool) ([]string, error) {
	sqlStr := "SELECT did FROM actor WHERE pds IS NULL OR pds = '' ORDER BY indexed_at"
	if force {
		sqlStr = "SELECT did FROM actor ORDER BY indexed_at"
	}
	rows, err := db.DB().QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dids []string
	for rows.Next() {
		var did string
		if err := rows.Scan(&did); err != nil {
			return nil, err
		}
		dids = append(dids, did)
	}
	return dids, rows.Err()
}
