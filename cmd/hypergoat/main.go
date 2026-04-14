// Package main is the entry point for the Hypergoat server.
//
// Hypergoat is a Go implementation of Quickslice - an AT Protocol AppView server
// that indexes Lexicon-defined records and exposes them via a dynamically-generated
// GraphQL API.
//
// For more information, see the README.md file.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/GainForest/hypergoat/internal/atproto"
	"github.com/GainForest/hypergoat/internal/backfill"
	"github.com/GainForest/hypergoat/internal/config"
	"github.com/GainForest/hypergoat/internal/database"
	"github.com/GainForest/hypergoat/internal/database/migrations"
	"github.com/GainForest/hypergoat/internal/database/repositories"
	hgraphql "github.com/GainForest/hypergoat/internal/graphql"
	"github.com/GainForest/hypergoat/internal/graphql/admin"
	"github.com/GainForest/hypergoat/internal/graphql/resolver"
	"github.com/GainForest/hypergoat/internal/graphql/subscription"
	"github.com/GainForest/hypergoat/internal/ingestion"
	"github.com/GainForest/hypergoat/internal/jetstream"
	"github.com/GainForest/hypergoat/internal/labeler"
	"github.com/GainForest/hypergoat/internal/lexicon"
	"github.com/GainForest/hypergoat/internal/metrics"
	"github.com/GainForest/hypergoat/internal/notifications"
	notifextractors "github.com/GainForest/hypergoat/internal/notifications/extractors"
	"github.com/GainForest/hypergoat/internal/oauth"
	"github.com/GainForest/hypergoat/internal/server"
	"github.com/GainForest/hypergoat/internal/tap"
	"github.com/GainForest/hypergoat/internal/workers"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Server failed", "error", err)
		os.Exit(1)
	}
}

// services holds all shared repositories and infrastructure dependencies.
// Created once in initServices and threaded through all setup functions,
// eliminating duplicate repository instantiation.
type services struct {
	db               database.Executor
	records          *repositories.RecordsRepository
	actors           *repositories.ActorsRepository
	lexicons         *repositories.LexiconsRepository
	config           *repositories.ConfigRepository
	activity         *repositories.JetstreamActivityRepository
	oauthClients     *repositories.OAuthClientsRepository
	labels           *repositories.LabelsRepository
	labelDefinitions *repositories.LabelDefinitionsRepository
	labelPreferences *repositories.LabelPreferencesRepository
	reports          *repositories.ReportsRepository
}

// backgroundServices tracks cancellable background goroutines for clean shutdown.
type backgroundServices struct {
	oauthCleanupCancel context.CancelFunc
	workersCancel      context.CancelFunc
	jsConsumer         *jetstream.Consumer
	jsCancel           context.CancelFunc
	tapCancel          context.CancelFunc

	// labelerMu guards the slice and the labeler cancel: startLabeler
	// appends at startup, backgroundServices.Stop iterates at shutdown,
	// and the /stats HTTP handler reads it at request time — all
	// potentially concurrently.
	labelerMu        sync.RWMutex
	labelerConsumers []*labeler.Consumer
	labelerCancel    context.CancelFunc

	backfillCancel context.CancelFunc
}

// Stop cleanly shuts down all background services.
func (bg *backgroundServices) Stop() {
	if bg.oauthCleanupCancel != nil {
		bg.oauthCleanupCancel()
	}
	if bg.workersCancel != nil {
		bg.workersCancel()
	}
	if bg.jsConsumer != nil {
		bg.jsConsumer.Stop()
	}
	if bg.jsCancel != nil {
		bg.jsCancel()
	}
	if bg.tapCancel != nil {
		bg.tapCancel()
	}
	// Snapshot labelers under the lock, then release before calling
	// Stop() on each — Stop blocks on the consumer's final flush and
	// we don't want to hold the slice lock across that.
	bg.labelerMu.Lock()
	consumers := bg.labelerConsumers
	bg.labelerConsumers = nil
	cancel := bg.labelerCancel
	bg.labelerCancel = nil
	bg.labelerMu.Unlock()

	for _, c := range consumers {
		c.Stop()
	}
	if cancel != nil {
		cancel()
	}
	if bg.backfillCancel != nil {
		bg.backfillCancel()
	}
}

// LabelerConsumers returns a snapshot of the currently-running labeler
// consumers under the read lock. Used by the /stats handler which
// needs a consistent view.
func (bg *backgroundServices) LabelerConsumers() []*labeler.Consumer {
	bg.labelerMu.RLock()
	defer bg.labelerMu.RUnlock()
	out := make([]*labeler.Consumer, len(bg.labelerConsumers))
	copy(out, bg.labelerConsumers)
	return out
}

func run() error {
	// Set up structured logging
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	slog.Info("Starting Hypergoat - AT Protocol AppView Server")

	// Load and validate configuration
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	cfg.LogConfig()

	// Initialize all services (DB, migrations, repositories)
	svc, err := initServices(cfg)
	if err != nil {
		return err
	}
	defer svc.db.Close()

	// Track background services for clean shutdown. Created before the
	// router so the /stats handler can capture a pointer to it.
	bg := &backgroundServices{}
	defer bg.Stop()

	// Set up HTTP router with middleware and basic endpoints
	r := setupRouter(cfg, svc, bg)

	// Set up OAuth endpoints
	setupOAuth(r, cfg, svc, bg)

	// Set up admin GraphQL endpoint with backfill callbacks
	adminHandler := setupAdmin(r, cfg, svc)

	// Load lexicons and set up public GraphQL + subscriptions
	pubsub := subscription.NewPubSub()
	collections, validator := setupGraphQL(r, cfg, svc, pubsub)

	// Configure backfill callbacks now that the validator exists.
	if adminHandler != nil {
		configureBackfillCallbacks(adminHandler, cfg, svc, validator)
	}

	// Start background workers (activity cleanup)
	startWorkers(svc, bg)

	// Start Jetstream consumer for real-time events
	startJetstream(cfg, svc, pubsub, collections, adminHandler, bg, validator)

	// Start labeler subscriptions (if any DIDs configured)
	startLabeler(cfg, svc, bg)

	// Start backfill if configured
	startBackfill(cfg, svc, bg, validator)

	// Run HTTP server with graceful shutdown
	return serve(r, cfg, bg)
}

// initServices connects to the database, runs migrations, and creates all
// repository instances. JetstreamActivityRepository is created once here
// instead of being duplicated across multiple call sites.
func initServices(cfg *config.Config) (*services, error) {
	db, err := server.ConnectDatabase(cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	slog.Info("Database connected successfully", "dialect", db.Dialect().String())

	slog.Info("Running database migrations...")
	if err := migrations.Run(context.Background(), db); err != nil {
		return nil, err
	}
	slog.Info("Database migrations complete")

	svc := &services{
		db:               db,
		records:          repositories.NewRecordsRepository(db),
		actors:           repositories.NewActorsRepository(db),
		lexicons:         repositories.NewLexiconsRepository(db),
		config:           repositories.NewConfigRepository(db),
		activity:         repositories.NewJetstreamActivityRepository(db),
		oauthClients:     repositories.NewOAuthClientsRepository(db),
		labels:           repositories.NewLabelsRepository(db),
		labelDefinitions: repositories.NewLabelDefinitionsRepository(db),
		labelPreferences: repositories.NewLabelPreferencesRepository(db),
		reports:          repositories.NewReportsRepository(db),
	}

	if cfg.PLCDirectoryURL != "" {
		svc.config.SetPLCDirectoryOverride(cfg.PLCDirectoryURL)
	}

	// Initialize config defaults and admin DIDs
	ctx := context.Background()
	if err := svc.config.InitializeDefaults(ctx); err != nil {
		slog.Warn("Failed to initialize config defaults", "error", err)
	}

	if adminDIDs := cfg.AdminDIDs; adminDIDs != "" {
		existingAdmins := svc.config.GetAdminDIDs(ctx)
		if len(existingAdmins) == 0 {
			if err := svc.config.Set(ctx, "admin_dids", adminDIDs); err != nil {
				slog.Warn("Failed to set admin_dids from environment", "error", err)
			} else {
				slog.Info("Initialized admin DIDs from environment", "dids", adminDIDs)
			}
		}
	}

	// Auto-populate activity from existing records if activity table is empty
	go populateActivityIfEmpty(ctx, svc)

	return svc, nil
}

// populateActivityIfEmpty creates activity entries from existing records when
// the activity table is empty but records exist (e.g., after a migration).
func populateActivityIfEmpty(ctx context.Context, svc *services) {
	recordCount, err := svc.records.GetCount(ctx)
	if err != nil {
		slog.Warn("Failed to get record count for activity population", "error", err)
		return
	}
	if recordCount == 0 {
		return
	}

	activityCount, err := svc.activity.GetCount(ctx)
	if err != nil {
		slog.Warn("Failed to get activity count", "error", err)
		return
	}
	if activityCount > 0 {
		return
	}

	slog.Info("Populating activity from existing records...", "record_count", recordCount)
	populated, err := populateActivityFromRecords(ctx, svc.records, svc.activity)
	if err != nil {
		slog.Error("Failed to populate activity", "error", err)
	} else {
		slog.Info("Activity populated from existing records", "count", populated)
	}
}

// setupRouter creates the chi router with middleware and basic HTTP endpoints
// (health, stats, root info, XRPC placeholder). The backgroundServices
// pointer is captured so the /stats handler can surface per-labeler
// counters at request time — the slice is populated later in startLabeler.
func setupRouter(cfg *config.Config, svc *services, bg *backgroundServices) *chi.Mux {
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))

	// CORS — uses AllowedOrigins from config; defaults to "*" if not set
	var allowedOrigins []string
	if cfg.AllowedOrigins != "" {
		for _, o := range strings.Split(cfg.AllowedOrigins, ",") {
			allowedOrigins = append(allowedOrigins, strings.TrimSpace(o))
		}
	}
	r.Use(server.CORSMiddleware(server.CORSConfig{
		AllowedOrigins: allowedOrigins,
		AdminAPIKeySet: cfg.AdminAPIKey != "",
	}))
	// Defensive response headers. HSTS is only emitted when
	// EXTERNAL_BASE_URL is https so a dev instance on http://
	// doesn't accidentally pin its own browser into HTTPS.
	httpsOnly := strings.HasPrefix(cfg.ExternalBaseURL, "https://")
	r.Use(server.SecurityHeadersMiddleware(httpsOnly))
	// Prometheus HTTP metrics middleware. Installed after chi's
	// RequestID / RealIP / Logger / Recoverer / Timeout so it sees
	// the dispatched route template for labelling.
	r.Use(metrics.Middleware)

	// Prometheus metrics endpoint. Unauthenticated by design —
	// metrics contain no PII and every series label is bounded.
	// Operators that want to gate /metrics should do it at the
	// reverse proxy (same place they'd gate it for any app).
	r.Handle("/metrics", metrics.Handler())

	// Health check. Must actually talk to the database so load
	// balancers stop routing to a degraded instance. We use a 2s
	// timeout so an unhealthy DB doesn't back up health checks.
	r.Get("/health", func(w http.ResponseWriter, req *http.Request) {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if err := svc.db.DB().PingContext(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status": "degraded",
				"error":  "database unreachable",
				"time":   time.Now().UTC().Format(time.RFC3339),
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status": "healthy",
			"time":   time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Labeler cursor reset — operator escape hatch to force a re-backfill
	// on the next startup for a specific labeler DID. Deletes both the
	// subscription seq cursor and any in-progress backfill checkpoint.
	// Gated behind ADMIN_API_KEY bearer auth (same mechanism as the
	// admin GraphQL handler) with constant-time comparison.
	// checkAdminBearer validates the ADMIN_API_KEY bearer token on a
	// raw HTTP admin endpoint. Returns true if the caller should
	// proceed; otherwise writes the error response and returns false.
	// Centralised here so every /admin/* raw HTTP route uses the same
	// constant-time comparison path instead of re-implementing it.
	checkAdminBearer := func(w http.ResponseWriter, req *http.Request) bool {
		if cfg.AdminAPIKey == "" {
			http.Error(w, "admin endpoint disabled: ADMIN_API_KEY is not configured", http.StatusForbidden)
			return false
		}
		auth := req.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return false
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		if subtle.ConstantTimeCompare([]byte(token), []byte(cfg.AdminAPIKey)) != 1 {
			http.Error(w, "invalid bearer token", http.StatusUnauthorized)
			return false
		}
		return true
	}

	r.Post("/admin/labeler/reset", func(w http.ResponseWriter, req *http.Request) {
		if !checkAdminBearer(w, req) {
			return
		}
		did := req.URL.Query().Get("did")
		if did == "" {
			http.Error(w, "missing did query parameter", http.StatusBadRequest)
			return
		}
		// Validate the DID format before using it as a config key —
		// otherwise an attacker with the API key could inject
		// arbitrary config-key shapes like `labeler_cursor:../..` and
		// delete unrelated rows.
		if !oauth.IsValidDID(did) {
			http.Error(w, "invalid did format (expected did:plc: or did:web:)", http.StatusBadRequest)
			return
		}
		reqCtx := req.Context()
		if err := svc.config.Delete(reqCtx, "labeler_cursor:"+did); err != nil {
			slog.Error("Labeler reset: failed to delete subscription cursor",
				"did", did, "error", err)
			http.Error(w, "failed to delete subscription cursor", http.StatusInternalServerError)
			return
		}
		if err := svc.config.Delete(reqCtx, "labeler_backfill_cursor:"+did); err != nil {
			slog.Error("Labeler reset: failed to delete backfill checkpoint",
				"did", did, "error", err)
			http.Error(w, "failed to delete backfill checkpoint", http.StatusInternalServerError)
			return
		}
		slog.Info("Labeler cursor reset by admin request",
			"did", did, "remote_addr", req.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reset": true,
			"did":   did,
			"note":  "restart the server to re-run backfill for this labeler",
		})
	})

	// Pause a single labeler subscription without restarting the
	// process. Calls Stop() on the matching consumer and removes
	// it from the labelerConsumers slice. The consumer's cursor is
	// flushed before Stop returns, so resume on next startup picks
	// up where it left off. A restart is still required to bring
	// the consumer back up (we do not currently support in-process
	// resume); this endpoint is for incident response.
	r.Post("/admin/labeler/pause", func(w http.ResponseWriter, req *http.Request) {
		if !checkAdminBearer(w, req) {
			return
		}
		did := req.URL.Query().Get("did")
		if did == "" {
			http.Error(w, "missing did query parameter", http.StatusBadRequest)
			return
		}
		if !oauth.IsValidDID(did) {
			http.Error(w, "invalid did format", http.StatusBadRequest)
			return
		}
		bg.labelerMu.Lock()
		var paused *labeler.Consumer
		remaining := bg.labelerConsumers[:0]
		for _, c := range bg.labelerConsumers {
			if c.LabelerDID() == did && paused == nil {
				paused = c
				continue
			}
			remaining = append(remaining, c)
		}
		bg.labelerConsumers = remaining
		bg.labelerMu.Unlock()

		if paused == nil {
			http.Error(w, "no active labeler consumer for this DID", http.StatusNotFound)
			return
		}
		paused.Stop()
		slog.Info("Labeler paused by admin request",
			"did", did, "remote_addr", req.RemoteAddr)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"paused": true,
			"did":    did,
			"note":   "restart the server to bring this labeler back up",
		})
	})

	// Label-chain inspection. Returns every label (active, negated,
	// expired) on a single URI with src + val + neg + cts + exp so
	// an operator can answer "why is this record hidden?" without
	// attaching a debugger. Deliberately bypasses the exp / neg
	// filters of the public query path — this is a diagnostic view.
	r.Get("/admin/label-chain", func(w http.ResponseWriter, req *http.Request) {
		if !checkAdminBearer(w, req) {
			return
		}
		uri := req.URL.Query().Get("uri")
		if uri == "" {
			http.Error(w, "missing uri query parameter", http.StatusBadRequest)
			return
		}
		rows, err := svc.labels.GetAllForURI(req.Context(), uri)
		if err != nil {
			slog.Error("label-chain lookup failed", "uri", uri, "error", err)
			http.Error(w, "failed to query labels", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"uri":    uri,
			"labels": rows,
		})
	})

	// Stats endpoint. Includes per-labeler consumer counters so operators
	// can inspect labeler ingestion health without attaching a debugger.
	r.Get("/stats", func(w http.ResponseWriter, req *http.Request) {
		reqCtx := req.Context()

		recordCount, err := svc.records.GetCount(reqCtx)
		if err != nil {
			slog.Error("Failed to get record count", "error", err)
			recordCount = -1
		}

		actorCount, err := svc.actors.GetCount(reqCtx)
		if err != nil {
			slog.Error("Failed to get actor count", "error", err)
			actorCount = -1
		}

		lexiconCount, err := svc.lexicons.GetCount(reqCtx)
		if err != nil {
			slog.Error("Failed to get lexicon count", "error", err)
			lexiconCount = -1
		}

		consumers := bg.LabelerConsumers()
		labelers := make([]map[string]any, 0, len(consumers))
		for _, c := range consumers {
			s := c.Stats()
			labelers = append(labelers, map[string]any{
				"did":                   c.LabelerDID(),
				"events_received":       s.EventsReceived,
				"labels_received":       s.LabelsReceived,
				"labels_persisted":      s.LabelsPersisted,
				"labels_rejected":       s.LabelsRejected,
				"account_level_skipped": s.AccountLevelSkipped,
				"outdated_cursors":      s.OutdatedCursors,
				"reconnect_attempts":    s.ReconnectAttempts,
				"last_seq":              s.LastSeq,
			})
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"records":  recordCount,
			"actors":   actorCount,
			"lexicons": lexiconCount,
			"labelers": labelers,
			"time":     time.Now().UTC().Format(time.RFC3339),
		})
	})

	// Root endpoint - server info
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"name":        "Hypergoat",
			"description": "AT Protocol AppView Server",
			"version":     "0.1.0-dev",
			"docs":        cfg.ExternalBaseURL + "/docs",
		})
	})

	// Placeholder for XRPC endpoints (AT Protocol)
	r.Route("/xrpc", func(r chi.Router) {
		r.Get("/*", func(w http.ResponseWriter, req *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotImplemented)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"error":   "NotImplemented",
				"message": "XRPC endpoints are not yet implemented",
			})
		})
	})

	return r
}

// setupOAuth registers all OAuth 2.0 endpoints (discovery, authorization flow,
// token management, DPoP, client registration, PAR) and starts the token
// cleanup worker.
func setupOAuth(r *chi.Mux, cfg *config.Config, svc *services, bg *backgroundServices) {
	oauthSigningKey, _ := oauth.GenerateDPoPKeyPair()
	if cfg.OAuthSigningKey != "" {
		if key, err := oauth.ParseDPoPKeyPair(cfg.OAuthSigningKey); err == nil {
			oauthSigningKey = key
		} else {
			slog.Warn("Failed to parse OAuth signing key, using ephemeral key", "error", err)
		}
	}

	oauthHandlers := server.NewOAuthHandlers(server.OAuthHandlerConfig{
		ExternalBaseURL:             cfg.ExternalBaseURL,
		ClientID:                    cfg.ExternalBaseURL + "/oauth-client-metadata.json",
		CallbackURL:                 cfg.ExternalBaseURL + "/oauth/callback",
		SigningKey:                  oauthSigningKey,
		Issuer:                      cfg.ExternalBaseURL,
		ScopesSupported:             []string{"atproto", "transition:generic", "transition:chat.bsky"},
		AccessTokenExpiration:       3600,    // 1 hour
		RefreshTokenExpiration:      1209600, // 14 days
		AuthorizationCodeExpiration: 600,     // 10 minutes
	}, svc.db)

	// Discovery endpoints
	r.Get("/.well-known/oauth-authorization-server", oauthHandlers.HandleAuthorizationServerMetadata)
	r.Get("/.well-known/oauth-protected-resource", oauthHandlers.HandleProtectedResourceMetadata)

	// Client metadata (this server as an OAuth client)
	r.Get("/oauth-client-metadata.json", server.HandleClientMetadata(server.ClientMetadataConfig{
		ExternalBaseURL: cfg.ExternalBaseURL,
		ClientName:      "Hypergoat",
		Scope:           "atproto transition:generic",
	}))

	// OAuth flow endpoints
	r.Get("/oauth/authorize", oauthHandlers.HandleAuthorize)
	r.Post("/oauth/authorize", oauthHandlers.HandleAuthorize)
	r.Get("/oauth/callback", oauthHandlers.HandleCallback)
	r.Post("/oauth/token", oauthHandlers.HandleToken)
	r.Get("/oauth/jwks", oauthHandlers.HandleJWKS)
	r.Post("/oauth/revoke", oauthHandlers.HandleRevoke)

	// Additional OAuth endpoints
	registerHandler := server.NewOAuthRegisterHandler(svc.db)
	r.Post("/oauth/register", registerHandler.HandleRegister)

	parHandler := server.NewOAuthPARHandler(svc.db)
	r.Post("/oauth/par", parHandler.HandlePAR)

	r.Get("/oauth/dpop/nonce", server.HandleDPoPNonce)
	r.Post("/oauth/dpop/nonce", server.HandleDPoPNonce)

	// Start cleanup worker
	oauthCleanupCtx, oauthCleanupCancel := context.WithCancel(context.Background())
	bg.oauthCleanupCancel = oauthCleanupCancel
	oauthHandlers.StartCleanupWorker(oauthCleanupCtx, 1*time.Hour)

	slog.Info("OAuth endpoints enabled",
		"authorization_server", cfg.ExternalBaseURL+"/.well-known/oauth-authorization-server",
		"client_metadata", cfg.ExternalBaseURL+"/oauth-client-metadata.json",
	)
}

// setupAdmin creates the admin GraphQL handler with backfill callbacks and
// registers admin routes + GraphiQL playgrounds. Returns the handler (or nil
// if setup fails) so callers can wire up the lexicon change callback.
func setupAdmin(r *chi.Mux, cfg *config.Config, svc *services) *admin.Handler {
	adminRepos := &admin.Repositories{
		Records:          svc.records,
		Actors:           svc.actors,
		Lexicons:         svc.lexicons,
		Config:           svc.config,
		OAuthClients:     svc.oauthClients,
		Activity:         svc.activity,
		Labels:           svc.labels,
		LabelDefinitions: svc.labelDefinitions,
		LabelPreferences: svc.labelPreferences,
		Reports:          svc.reports,
	}

	authMiddleware := oauth.NewAuthMiddleware(
		repositories.NewOAuthAccessTokensRepository(svc.db),
		repositories.NewOAuthDPoPJTIRepository(svc.db),
		cfg.ExternalBaseURL,
	)

	domainDID := cfg.DomainDID
	if domainDID == "" {
		domainDID = "did:web:" + cfg.Host
	}

	var adminOpts []admin.HandlerOption
	if cfg.NotificationsEnabled {
		notifRepo := notifications.NewRepository(svc.db)
		notifResolver := notifications.NewResolver(notifRepo)
		adminOpts = append(adminOpts,
			admin.WithExtraQueries(notifResolver.QueryFields()),
			admin.WithExtraMutations(notifResolver.MutationFields()),
		)
	}

	adminHandler, err := admin.NewHandler(adminRepos, authMiddleware, svc.config, domainDID, cfg.AdminAPIKey, adminOpts...)
	if err != nil {
		slog.Error("Failed to create admin GraphQL handler", "error", err)
		return nil
	}

	// Wire up backfill callbacks for the admin UI
	// Backfill callbacks are configured later, after the validator is
	// created in setupGraphQL. See run() after setupGraphQL call.

	// Admin endpoint with optional auth (allows introspection without auth)
	r.Handle("/admin/graphql", adminHandler.OptionalAuth())
	r.Handle("/admin/graphql/", adminHandler.OptionalAuth())
	slog.Info("Admin GraphQL endpoint enabled", "path", "/admin/graphql")

	// GraphiQL playgrounds
	r.Get("/graphiql", server.HandleGraphiQL(server.GraphiQLConfig{
		EndpointPath:     "/graphql",
		SubscriptionPath: "/graphql/ws",
		Title:            "Hypergoat GraphQL",
		DefaultQuery: `# Hypergoat GraphQL API
# 
# Explore the AT Protocol data indexed by this AppView.
# Try querying for records from your configured lexicons.
#
# Example:
{
  __schema {
    types {
      name
    }
  }
}
`,
	}))

	r.Get("/graphiql/admin", server.HandleGraphiQL(server.GraphiQLConfig{
		EndpointPath: "/admin/graphql",
		Title:        "Hypergoat Admin",
		AdminAuth:    true,
		DefaultQuery: `# Hypergoat Admin API
#
# Administrative operations for managing the AppView.
# Enter your API Key and DID above to authenticate.
#
# Example:
{
  statistics {
    recordCount
    actorCount
    lexiconCount
  }
}
`,
	}))

	slog.Info("GraphiQL playgrounds enabled",
		"public", cfg.ExternalBaseURL+"/graphiql",
		"admin", cfg.ExternalBaseURL+"/graphiql/admin",
	)

	return adminHandler
}

// configureBackfillCallbacks sets up single-actor and full-network backfill
// callbacks on the admin handler's resolver, used by the admin UI.
func configureBackfillCallbacks(adminHandler *admin.Handler, cfg *config.Config, svc *services, validator *lexicon.Validator) {
	bfConfig := backfill.NewConfigFromApp(cfg)
	if bfConfig.Collections == nil {
		bfConfig.Collections = atproto.ParseCollections(cfg.JetstreamCollections)
	}

	actorBackfiller := backfill.NewBackfiller(bfConfig, svc.records, svc.actors, svc.activity, validator, cfg.ValidationMode)

	// Single actor backfill
	adminHandler.Resolver().SetBackfillCallback(func(ctx context.Context, did string) error {
		_, err := actorBackfiller.BackfillActor(ctx, did)
		return err
	})

	// Full network backfill (runs in background)
	adminHandler.Resolver().SetFullBackfillCallback(func(ctx context.Context) error {
		collections := bfConfig.Collections
		if len(collections) == 0 {
			lexicons, err := svc.lexicons.GetAll(ctx)
			if err != nil {
				slog.Error("[backfill] Failed to get lexicons", "error", err)
				return err
			}
			for _, lex := range lexicons {
				collections = append(collections, lex.ID)
			}
		}

		if len(collections) == 0 {
			slog.Warn("[backfill] No collections configured - register lexicons first or set BACKFILL_COLLECTIONS")
			return nil
		}

		fullConfig := bfConfig
		fullConfig.Collections = collections
		bf := backfill.NewBackfiller(fullConfig, svc.records, svc.actors, svc.activity, validator, cfg.ValidationMode)
		defer bf.Close()

		slog.Info("[backfill] Starting full network backfill", "collections", collections)
		stats, err := bf.Run(ctx)
		if err != nil {
			slog.Error("[backfill] Full backfill failed", "error", err)
			return err
		}
		slog.Info("[backfill] Full backfill completed",
			"repos_discovered", stats.ReposDiscovered,
			"repos_processed", stats.ReposProcessed,
			"records_inserted", stats.RecordsInserted,
			"duration", stats.Duration(),
		)
		return nil
	})

	slog.Info("Backfill callbacks configured for admin UI")
}

// setupGraphQL loads lexicons from disk and database, creates the public GraphQL
// handler with WebSocket subscriptions, and returns the resolved collection list
// for Jetstream configuration.
func setupGraphQL(r *chi.Mux, cfg *config.Config, svc *services, pubsub *subscription.PubSub) ([]string, *lexicon.Validator) {
	// Load lexicons from filesystem
	registry := lexicon.NewRegistry()
	lexiconDir := cfg.LexiconDir
	if lexiconDir == "" {
		lexiconDir = "testdata/lexicons"
	}

	if _, err := os.Stat(lexiconDir); err == nil {
		if err := loadLexiconsFromDir(lexiconDir, registry); err != nil {
			slog.Warn("Failed to load lexicons from directory", "dir", lexiconDir, "error", err)
		} else {
			slog.Info("Loaded lexicons from directory", "count", registry.Count(), "dir", lexiconDir)
		}
	}

	// Load lexicons from database (uploaded via admin UI)
	ctx := context.Background()
	dbLexicons, err := svc.lexicons.GetAll(ctx)
	if err != nil {
		slog.Warn("Failed to load lexicons from database", "error", err)
	} else if len(dbLexicons) > 0 {
		dbLoaded := 0
		for _, dbLex := range dbLexicons {
			if _, err := registry.ParseAndRegister(dbLex.JSON); err != nil {
				slog.Warn("Failed to parse database lexicon", "id", dbLex.ID, "error", err)
			} else {
				dbLoaded++
			}
		}
		slog.Info("Loaded lexicons from database", "count", dbLoaded, "total", len(dbLexicons))
	}

	slog.Info("Total lexicons registered", "count", registry.Count())

	// Create GraphQL handler. The indexer is neutral about which
	// labeler is authoritative — no DefaultLabelerDID is passed in,
	// so label-filtered queries match any labeler by default and
	// clients narrow via the labelerDids arg.
	repos := &resolver.Repositories{
		Records:  svc.records,
		Actors:   svc.actors,
		Lexicons: svc.lexicons,
		Labels:   svc.labels,
	}

	graphqlHandler, err := hgraphql.NewHandler(registry, repos)
	if err != nil {
		slog.Error("Failed to create GraphQL handler", "error", err)
	} else {
		r.Handle("/graphql", graphqlHandler)
		r.Handle("/graphql/", graphqlHandler)
		slog.Info("GraphQL endpoint enabled", "path", "/graphql")

		// WebSocket subscription endpoint
		var allowedOrigins []string
		if cfg.AllowedOrigins != "" {
			allowedOrigins = strings.Split(cfg.AllowedOrigins, ",")
			for i := range allowedOrigins {
				allowedOrigins[i] = strings.TrimSpace(allowedOrigins[i])
			}
		}
		subscriptionHandler := subscription.NewHandler(graphqlHandler.Schema(), pubsub, allowedOrigins)
		r.Handle("/graphql/ws", subscriptionHandler)
		slog.Info("GraphQL subscriptions enabled", "path", "/graphql/ws")
	}

	// Resolve collections for Jetstream
	var collections []string
	if cfg.JetstreamCollections != "" {
		collections = atproto.ParseCollections(cfg.JetstreamCollections)
	} else {
		for _, lex := range dbLexicons {
			collections = append(collections, lex.ID)
		}
	}

	return collections, lexicon.NewValidator(registry)
}

// startWorkers launches background worker goroutines (activity cleanup).
func startWorkers(svc *services, bg *backgroundServices) {
	activityCleanupWorker := workers.NewActivityCleanupWorker(svc.activity)
	workersCtx, workersCancel := context.WithCancel(context.Background())
	bg.workersCancel = workersCancel
	activityCleanupWorker.Start(workersCtx)
}

// startJetstream creates and starts the Jetstream consumer for real-time AT Protocol
// events. It also wires up the lexicon change callback on the admin handler so that
// adding/removing lexicons dynamically updates the consumer's collection filter.
func startJetstream(
	cfg *config.Config,
	svc *services,
	pubsub *subscription.PubSub,
	collections []string,
	adminHandler *admin.Handler,
	bg *backgroundServices,
	validator *lexicon.Validator,
) {
	jsURL := cfg.JetstreamURL
	if jsURL == "" {
		jsURL = jetstream.DefaultJetstreamURL
	}

	// Build shared record processor for all consumers (Jetstream or Tap).
	processor := &ingestion.RecordProcessor{
		Records:   svc.records,
		Actors:    svc.actors,
		Activity:  svc.activity,
		PubSub:    pubsub,
		Validator: validator,
		ValMode:   cfg.ValidationMode,
	}

	// Attach notifications hook if enabled.
	if cfg.NotificationsEnabled {
		notifRepo := notifications.NewRepository(svc.db)
		notifService := notifications.NewService(notifRepo)
		notifService.Register(notifextractors.NewEndorsementNotifier())
		notifService.Register(notifextractors.NewActivityContributorNotifier())
		processor.RecordHooks = append(processor.RecordHooks, notifService.Hook())
		slog.Info("Notifications subsystem enabled",
			"extractors", []string{"endorsement", "activity-contributor"})
	}

	// If Tap is enabled, start the Tap consumer instead of Jetstream.
	if cfg.TapEnabled {
		// Build collection allowlist from config.
		if cfg.TapCollectionFilters != "" {
			allowedCollections := make(map[string]bool)
			for _, col := range strings.Split(cfg.TapCollectionFilters, ",") {
				col = strings.TrimSpace(col)
				if col != "" {
					allowedCollections[col] = true
				}
			}
			processor.AllowedCollections = allowedCollections
		}

		handler := tap.NewIndexHandler(processor, svc.actors)
		tapConsumer := tap.NewConsumer(tap.ConsumerConfig{
			TapURL:      cfg.TapURL,
			DisableAcks: cfg.TapDisableAcks,
			MaxRetries:  cfg.TapMaxRetries,
		}, handler)

		tapCtx, tapCancel := context.WithCancel(context.Background())
		bg.tapCancel = tapCancel

		go func() {
			slog.Info("Starting Tap consumer",
				"url", cfg.TapURL,
				"disable_acks", cfg.TapDisableAcks,
			)
			if err := tapConsumer.Start(tapCtx); err != nil {
				slog.Error("Tap consumer error", "error", err)
			}
		}()

		slog.Info("Tap consumer started (Jetstream disabled)")
		return
	}

	if len(collections) > 0 {
		bg.jsConsumer = jetstream.NewConsumer(
			jetstream.ConsumerConfig{
				JetstreamURL:  jsURL,
				Collections:   collections,
				DisableCursor: cfg.JetstreamDisableCursor,
			},
			processor,
			svc.config,
		)

		jsCtx, jsCancel := context.WithCancel(context.Background())
		bg.jsCancel = jsCancel

		go func() {
			slog.Info("Starting Jetstream consumer",
				"url", jsURL,
				"collections", collections,
				"disable_cursor", cfg.JetstreamDisableCursor,
			)
			if err := bg.jsConsumer.Start(jsCtx); err != nil {
				slog.Error("Jetstream consumer error", "error", err)
			}
		}()
	} else {
		slog.Info("Jetstream consumer disabled (no collections - register lexicons or set JETSTREAM_COLLECTIONS)")
	}

	// Wire up lexicon change callback for dynamic Jetstream updates
	if adminHandler != nil {
		adminHandler.Resolver().SetLexiconChangeCallback(func(updatedCollections []string) error {
			if bg.jsConsumer == nil {
				bg.jsConsumer = jetstream.NewConsumer(
					jetstream.ConsumerConfig{
						JetstreamURL:  jsURL,
						Collections:   updatedCollections,
						DisableCursor: cfg.JetstreamDisableCursor,
					},
					processor,
					svc.config,
				)

				// Use a tracked context derived from a fresh
				// cancel func so graceful shutdown still stops
				// this dynamically-created consumer.
				dynCtx, dynCancel := context.WithCancel(context.Background())
				bg.jsCancel = dynCancel

				go func() {
					slog.Info("Starting Jetstream consumer (dynamic)",
						"collections", updatedCollections,
					)
					if err := bg.jsConsumer.Start(dynCtx); err != nil {
						slog.Error("Jetstream consumer error", "error", err)
					}
				}()
				return nil
			}
			// Pass context.Background as the parent here: we have no
			// long-lived ctx in scope in the callback, and Stop on
			// the consumer still tears it down via its own tracked
			// c.ctxCancel, which bg.Stop() invokes. Keeping this
			// explicit so the choice is visible.
			return bg.jsConsumer.UpdateCollections(context.Background(), updatedCollections)
		})
		slog.Info("Lexicon change callback configured for dynamic Jetstream updates")
	}
}

// startLabeler starts one labeler.Consumer per configured DID, resolving
// each labeler's endpoint via the OAuth DIDResolver (which respects the
// PLC_DIRECTORY_URL override). Invalid DIDs are skipped with a warning.
// Consumers are added to backgroundServices for graceful shutdown.
//
// Respects two additional config knobs:
//   - LabelerDryRun: only resolves labeler hosts and logs the results,
//     does not spawn any consumers or ingest any labels. Useful for
//     validating configuration in staging without committing state.
//   - LabelerCursorFlushInterval: seconds between cursor flushes
//     (0 = package default of 5s).
func startLabeler(cfg *config.Config, svc *services, bg *backgroundServices) {
	raw := parseDIDs(cfg.LabelerDIDs)
	if len(raw) == 0 {
		slog.Info("Labeler subscriptions disabled (LABELER_DIDS is empty)")
		return
	}

	var dids []string
	for _, d := range raw {
		if !oauth.IsValidDID(d) {
			slog.Warn("Ignoring invalid labeler DID",
				"did", d,
				"hint", "expected did:plc: or did:web:")
			continue
		}
		dids = append(dids, d)
	}
	if len(dids) == 0 {
		slog.Warn("LABELER_DIDS set but all entries are invalid; no labeler consumers will run")
		return
	}

	var resolverOpts []oauth.DIDResolverOption
	if cfg.PLCDirectoryURL != "" {
		resolverOpts = append(resolverOpts, oauth.WithPLCDirectoryURL(cfg.PLCDirectoryURL))
	}
	didResolver := oauth.NewDIDResolver(resolverOpts...)

	if cfg.LabelerDryRun {
		for _, did := range dids {
			doc, err := didResolver.ResolveDID(did)
			if err != nil {
				slog.Error("Labeler dry run: DID resolution failed",
					"did", did, "error", err)
				continue
			}
			host := doc.GetLabelerEndpoint()
			svcType := "AtprotoLabeler"
			if host == "" {
				host = doc.GetPDSEndpoint()
				svcType = "AtprotoPersonalDataServer (fallback)"
			}
			slog.Info("Labeler dry run: resolved",
				"did", did, "host", host, "service", svcType)
		}
		slog.Info("Labeler dry run complete; not starting consumers (LABELER_DRY_RUN=true)")
		return
	}

	ctx, cancel := context.WithCancel(context.Background())

	bg.labelerMu.Lock()
	bg.labelerCancel = cancel
	for _, did := range dids {
		cc := labeler.ConsumerConfig{LabelerDID: did}
		if cfg.LabelerCursorFlushInterval > 0 {
			cc.CursorFlushInterval = time.Duration(cfg.LabelerCursorFlushInterval) * time.Second
		}
		consumer := labeler.NewConsumer(
			cc,
			svc.labels,
			svc.labelDefinitions,
			svc.config,
			didResolver,
		)
		bg.labelerConsumers = append(bg.labelerConsumers, consumer)
	}
	consumers := make([]*labeler.Consumer, len(bg.labelerConsumers))
	copy(consumers, bg.labelerConsumers)
	bg.labelerMu.Unlock()

	for _, c := range consumers {
		go func(c *labeler.Consumer) {
			did := c.LabelerDID()
			// Panic recovery on the long-running consumer goroutine.
			// A panic here (e.g., nil deref in label processing)
			// should not take down the whole process — log it and
			// let the parent lifecycle restart us via Stop/Start on
			// the next reconcile.
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("Labeler consumer goroutine panicked",
						"did", did, "panic", rec)
				}
			}()
			slog.Info("Starting labeler subscription", "did", did)
			if err := c.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Error("Labeler subscription error", "did", did, "error", err)
			}
		}(c)
	}
}

// startBackfill runs the initial backfill in the background if
// BACKFILL_ON_START is set. The goroutine gets a tracked cancel so
// graceful shutdown can interrupt an in-progress backfill instead of
// hanging indefinitely on bg.Stop.
func startBackfill(cfg *config.Config, svc *services, bg *backgroundServices, validator *lexicon.Validator) {
	if !cfg.BackfillOnStart {
		return
	}

	bfConfig := backfill.NewConfigFromApp(cfg)
	if bfConfig.Collections == nil {
		bfConfig.Collections = atproto.ParseCollections(cfg.JetstreamCollections)
	}

	if len(bfConfig.Collections) == 0 {
		slog.Warn("BACKFILL_ON_START=true but no collections specified")
		return
	}

	backfiller := backfill.NewBackfiller(bfConfig, svc.records, svc.actors, svc.activity, validator, cfg.ValidationMode)

	bfCtx, bfCancel := context.WithCancel(context.Background())
	bg.backfillCancel = bfCancel

	go func() {
		slog.Info("Starting backfill operation",
			"collections", bfConfig.Collections,
			"relay", bfConfig.RelayURL,
		)
		stats, err := backfiller.Run(bfCtx)
		if err != nil {
			slog.Error("Backfill failed", "error", err)
		} else {
			slog.Info("Backfill completed",
				"repos_discovered", stats.ReposDiscovered,
				"repos_processed", stats.ReposProcessed,
				"records_inserted", stats.RecordsInserted,
				"duration", stats.Duration(),
			)
		}
	}()
}

// serve starts the HTTP server and blocks until a shutdown signal is received,
// then performs a graceful shutdown with a 30-second timeout.
func serve(r *chi.Mux, cfg *config.Config, bg *backgroundServices) error {
	srv := &http.Server{
		Addr:        cfg.Address(),
		Handler:     r,
		ReadTimeout: 15 * time.Second,
		// WriteTimeout disabled (set to 0) to support long-lived WebSocket connections.
		// Individual handlers enforce their own write deadlines:
		// - WebSocket: per-message deadline in subscription/handler.go
		// - HTTP: standard response lifecycle
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
	}

	// Start server in a goroutine
	serverErr := make(chan error, 1)
	go func() {
		slog.Info("Server listening", "address", cfg.Address(), "url", cfg.ExternalBaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	// Wait for interrupt signal or server error
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		return err
	case <-quit:
	}

	slog.Info("Shutting down server...")

	// Drain in-flight HTTP requests first so the server stops
	// accepting new work. Background services are still up at this
	// point, which lets already-running GraphQL queries finish against
	// a valid DB and consumer state. Once HTTP is drained we stop the
	// background services (which flushes cursors, etc.).
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	httpErr := srv.Shutdown(shutdownCtx)
	bg.Stop()
	if httpErr != nil {
		return httpErr
	}

	slog.Info("Server stopped gracefully")
	return nil
}

// parseDIDs splits a comma-separated list of DIDs and trims whitespace.
func parseDIDs(commaSeparated string) []string {
	var out []string
	for _, s := range strings.Split(commaSeparated, ",") {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// loadLexiconsFromDir loads all lexicon JSON files from a directory tree.
func loadLexiconsFromDir(dir string, registry *lexicon.Registry) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		lex, parseErr := lexicon.ParseBytes(data)
		if parseErr != nil {
			// Skip non-lexicon JSON files
			return nil //nolint:nilerr // intentionally skip parse errors
		}

		registry.Register(lex)
		return nil
	})
}

// populateActivityFromRecords creates activity entries from existing records.
func populateActivityFromRecords(
	ctx context.Context,
	recordsRepo *repositories.RecordsRepository,
	activityRepo *repositories.JetstreamActivityRepository,
) (int64, error) {
	var count int64
	_, err := recordsRepo.IterateAll(ctx, 1000, func(rec *repositories.Record) error {
		// Extract createdAt from the record JSON, fall back to IndexedAt
		timestamp := atproto.ExtractCreatedAt(rec.JSON, rec.IndexedAt)

		// Log as a successful create operation
		if _, logErr := activityRepo.LogActivityWithStatus(ctx, timestamp, "create", rec.Collection, rec.DID, rec.RKey, rec.JSON, "success"); logErr == nil {
			count++
		}
		return nil
	})
	return count, err
}
