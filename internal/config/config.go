// Package config handles application configuration loading from environment variables.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Config holds all application configuration.
type Config struct {
	// Server configuration
	Host string
	Port int

	// Database
	DatabaseURL string

	// Security
	SecretKeyBase  string
	AllowedOrigins string // Comma-separated allowed WebSocket/CORS origins (empty = allow all origins, set explicitly for production)

	// OAuth
	ExternalBaseURL   string
	OAuthSigningKey   string
	OAuthLoopbackMode bool
	// OAuthLegacyDPoPJKTCutoff is the Unix timestamp past which refresh tokens
	// without a DPoP JKT binding are rejected on refresh (issue #24). Set this
	// to the deploy time of the binding feature so existing tokens issued
	// before then keep working until they expire, while any token issued
	// after this point must be JKT-bound. Required — the server refuses to
	// start without it (fail-closed).
	OAuthLegacyDPoPJKTCutoff int64

	// Admin
	AdminDIDs   string // Comma-separated list of admin DIDs
	AdminAPIKey string // Shared secret; when set, X-User-DID header is trusted if accompanied by a valid Bearer token
	DomainDID   string // Domain DID for identity

	// Lexicons
	LexiconDir string // Directory to load lexicon JSON files from

	// Jetstream
	JetstreamURL           string // Jetstream WebSocket URL
	JetstreamCollections   string // Comma-separated collections to subscribe to
	JetstreamDisableCursor bool   // Disable cursor-based resume

	// Tap (alternative to Jetstream — crypto-verified events)
	TapEnabled           bool   // Use Tap instead of Jetstream
	TapURL               string // Tap WebSocket URL (default wss://localhost:2480)
	TapAdminPassword     string // Password for Tap admin API
	TapDisableAcks       bool   // Fire-and-forget mode
	TapCollectionFilters string // Comma-separated collection NSIDs
	TapMaxRetries        int    // Per-event retry limit (default 3)

	// Backfill
	BackfillOnStart           bool   // Run backfill on server start
	BackfillCollections       string // Comma-separated collections to backfill (defaults to JetstreamCollections)
	BackfillRelayURL          string // AT Protocol relay URL
	BackfillPLCURL            string // PLC directory URL
	BackfillPDSConcurrency    int
	BackfillMaxPDSWorkers     int
	BackfillMaxHTTPConcurrent int
	BackfillMaxPerPDS         int
	BackfillMaxRepos          int
	BackfillRepoTimeoutMS     int

	// PLC Directory
	PLCDirectoryURL string // PLC directory URL for DID resolution

	// Labelers
	LabelerDIDs                string // Comma-separated labeler DIDs to subscribe to (optional)
	LabelerDryRun              bool   // If true, resolve labeler hosts and exit without ingesting
	LabelerCursorFlushInterval int    // Cursor flush cadence in seconds (0 = default 5s)

	// Validation
	ValidationMode string // Record validation mode: "disabled" (default), "warn" (log but store), "enforce" (reject invalid records)

	// Notifications
	NotificationsEnabled bool // Enable the notifications subsystem (default false)
}

// Load reads configuration from environment variables.
// It loads .env file if present and applies defaults.
func Load() (*Config, error) {
	// Load .env file if it exists (ignore error if not found)
	_ = godotenv.Load()

	cfg := &Config{
		// Server
		Host: getEnv("HOST", "127.0.0.1"),
		Port: getEnvInt("PORT", 8080),

		// Database
		DatabaseURL: getEnv("DATABASE_URL", ""),

		// Security
		SecretKeyBase:  getEnv("SECRET_KEY_BASE", ""),
		AllowedOrigins: getEnv("ALLOWED_ORIGINS", ""),

		// OAuth
		ExternalBaseURL:          getEnv("EXTERNAL_BASE_URL", ""),
		OAuthSigningKey:          getEnv("OAUTH_SIGNING_KEY", ""),
		OAuthLoopbackMode:        getEnvBool("OAUTH_LOOPBACK_MODE", false),
		OAuthLegacyDPoPJKTCutoff: getEnvInt64("OAUTH_LEGACY_DPOP_JKT_CUTOFF", 0),

		// Admin
		AdminDIDs:   getEnv("ADMIN_DIDS", ""),
		AdminAPIKey: getEnv("ADMIN_API_KEY", ""),
		DomainDID:   getEnv("DOMAIN_DID", ""),

		// Lexicons
		LexiconDir: getEnv("LEXICON_DIR", ""),

		// Jetstream
		JetstreamURL:           getEnv("JETSTREAM_URL", ""),
		JetstreamCollections:   getEnv("JETSTREAM_COLLECTIONS", ""),
		JetstreamDisableCursor: getEnvBool("JETSTREAM_DISABLE_CURSOR", false),

		// Tap
		TapEnabled:           getEnvBool("TAP_ENABLED", false),
		TapURL:               getEnv("TAP_URL", "wss://localhost:2480"),
		TapAdminPassword:     getEnv("TAP_ADMIN_PASSWORD", ""),
		TapDisableAcks:       getEnvBool("TAP_DISABLE_ACKS", false),
		TapCollectionFilters: getEnv("TAP_COLLECTION_FILTERS", ""),
		TapMaxRetries:        getEnvInt("TAP_MAX_RETRIES", 3),

		// Backfill
		BackfillOnStart:           getEnvBool("BACKFILL_ON_START", false),
		BackfillCollections:       getEnv("BACKFILL_COLLECTIONS", ""),
		BackfillRelayURL:          getEnv("BACKFILL_RELAY_URL", ""),
		BackfillPLCURL:            getEnv("BACKFILL_PLC_URL", ""),
		BackfillPDSConcurrency:    getEnvInt("BACKFILL_PDS_CONCURRENCY", 4),
		BackfillMaxPDSWorkers:     getEnvInt("BACKFILL_MAX_PDS_WORKERS", 10),
		BackfillMaxHTTPConcurrent: getEnvInt("BACKFILL_MAX_HTTP", 50),
		BackfillMaxPerPDS:         getEnvInt("BACKFILL_MAX_PER_PDS", 6),
		BackfillMaxRepos:          getEnvInt("BACKFILL_MAX_REPOS", 50),
		BackfillRepoTimeoutMS:     getEnvInt("BACKFILL_REPO_TIMEOUT", 60000),

		// PLC Directory
		PLCDirectoryURL: getEnv("PLC_DIRECTORY_URL", ""),

		// Labelers
		LabelerDIDs:                getEnv("LABELER_DIDS", ""),
		LabelerDryRun:              getEnvBool("LABELER_DRY_RUN", false),
		LabelerCursorFlushInterval: getEnvInt("LABELER_CURSOR_FLUSH_INTERVAL", 0),
		ValidationMode:             getEnv("VALIDATION_MODE", "disabled"),

		NotificationsEnabled: getEnvBool("NOTIFICATIONS_ENABLED", false),
	}

	// Generate SecretKeyBase if not provided
	if cfg.SecretKeyBase == "" {
		slog.Warn("SECRET_KEY_BASE not set, generating random key",
			"warning", "Sessions will be invalidated on server restart")
		key, err := generateRandomKey(64)
		if err != nil {
			return nil, fmt.Errorf("failed to generate secret key: %w", err)
		}
		cfg.SecretKeyBase = key
	}

	// Normalize EXTERNAL_BASE_URL and fall back to the host:port
	// default when unset. Operators set this to bare hosts
	// (`magic-indexer.example.com`), uppercased schemes
	// (`HTTPS://…`), or values with trailing slashes more often
	// than we'd like; without normalization those produce broken
	// OAuth redirect URIs and doubled-host GraphiQL endpoints.
	cfg.ExternalBaseURL = normalizeExternalBaseURL(cfg.ExternalBaseURL, cfg.Host, cfg.Port)

	return cfg, nil
}

// normalizeExternalBaseURL canonicalises an operator-supplied
// EXTERNAL_BASE_URL. Rules, in order:
//
//  1. Trim surrounding whitespace.
//  2. Empty → fall back to `http://<host>:<port>`.
//  3. If a scheme is present, lowercase it (case-insensitive match)
//     so HSTS gating and string-prefix comparisons elsewhere don't
//     silently fall off the happy path.
//  4. If no scheme is present, infer one: loopback hosts
//     (`localhost`, `127.0.0.1`, `::1`, with or without port) get
//     `http://`; everything else gets `https://`. The loopback
//     carve-out matters because bare `localhost:8080` in a dev
//     `.env` would otherwise become `https://localhost:8080` and
//     boot a TLS-only server that the dev's browser can't talk to.
//  5. Trim any trailing slash so endpoints built by raw
//     concatenation (`cfg.ExternalBaseURL + "/oauth/callback"`)
//     don't produce `//oauth/callback` and mismatch the registered
//     OAuth redirect URI.
func normalizeExternalBaseURL(raw, host string, port int) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return fmt.Sprintf("http://%s:%d", host, port)
	}

	// Lowercase scheme if present.
	if idx := strings.Index(v, "://"); idx > 0 {
		scheme := strings.ToLower(v[:idx])
		v = scheme + v[idx:]
	} else {
		// No scheme — infer one from the host.
		hostPart := v
		if i := strings.Index(v, "/"); i >= 0 {
			hostPart = v[:i]
		}
		// Strip port for the loopback check.
		hostOnly := hostPart
		if strings.HasPrefix(hostOnly, "[") {
			// IPv6 literal like `[::1]:8080` or `[::1]`.
			if end := strings.Index(hostOnly, "]"); end > 0 {
				hostOnly = hostOnly[1:end]
			}
		} else if i := strings.LastIndex(hostOnly, ":"); i >= 0 {
			hostOnly = hostOnly[:i]
		}
		hostOnly = strings.ToLower(hostOnly)

		if hostOnly == "localhost" || hostOnly == "127.0.0.1" || hostOnly == "::1" {
			v = "http://" + v
		} else {
			v = "https://" + v
		}
	}

	return strings.TrimRight(v, "/")
}

// devSecretKeyBase is the literal placeholder value shipped in
// docker-compose.yml and the example env file. Any deployment that
// boots with this exact string is misconfigured — session tokens
// would be forged-able by anyone who's read the repo. Validate()
// refuses to start in that state.
const devSecretKeyBase = "development-secret-key-change-in-production-64chars"

// Validate checks that all required configuration is present and valid.
func (c *Config) Validate() error {
	if len(c.SecretKeyBase) < 64 {
		return fmt.Errorf("SECRET_KEY_BASE must be at least 64 characters")
	}
	if c.SecretKeyBase == devSecretKeyBase {
		return fmt.Errorf(
			"SECRET_KEY_BASE is set to the docker-compose development placeholder; " +
				"set it to a real random value (e.g. openssl rand -base64 64)",
		)
	}

	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("PORT must be between 1 and 65535")
	}

	// OAuth DPoP JKT binding cutoff (issue #24) — fail-closed. If unset or
	// non-positive the server can't tell legacy tokens from post-binding
	// tokens, so refuse to start rather than silently accepting unbound
	// refresh attempts.
	if c.OAuthLegacyDPoPJKTCutoff <= 0 {
		return fmt.Errorf(
			"OAUTH_LEGACY_DPOP_JKT_CUTOFF must be set to the Unix timestamp of the DPoP-binding deploy; " +
				"refresh tokens issued before this cutoff are accepted unbound, tokens after must be JKT-bound",
		)
	}

	// DOMAIN_DID gates the /notifications/graphql service-auth endpoint
	// (issue #57). Unset is allowed — the endpoint simply stays
	// unmounted — but a malformed value is fatal: it's the `aud` every
	// token is checked against, so typoing it silently would 404 every
	// honest caller.
	if c.DomainDID != "" && !looksLikeDID(c.DomainDID) {
		return fmt.Errorf(
			"DOMAIN_DID = %q is not a valid DID (must start with did:plc: or did:web:)",
			c.DomainDID,
		)
	}

	return nil
}

// looksLikeDID is a cheap syntactic check we can run at config load,
// targeted at the "typo the env var" case. The canonical strict DID
// validator is `internal/atproto/did.IsValid`; this looser check
// stays here because config loading just needs to flag obvious
// nonsense before deeper components see the value.
func looksLikeDID(s string) bool {
	return strings.HasPrefix(s, "did:plc:") || strings.HasPrefix(s, "did:web:")
}

// labelerDIDsCount returns the number of non-empty entries in a
// comma-separated DID list. Used when logging so we surface "how
// many" without dumping the list of DIDs into the log stream.
func labelerDIDsCount(raw string) int {
	if raw == "" {
		return 0
	}
	n := 0
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) != "" {
			n++
		}
	}
	return n
}

// LogConfig logs the configuration (with sensitive values redacted).
func (c *Config) LogConfig() {
	slog.Info("Configuration loaded",
		"host", c.Host,
		"port", c.Port,
		"database_url", RedactPassword(c.DatabaseURL),
		"external_base_url", c.ExternalBaseURL,
		"oauth_loopback_mode", c.OAuthLoopbackMode,
		"oauth_signing_key_set", c.OAuthSigningKey != "",
		"admin_dids_set", c.AdminDIDs != "",
		"admin_api_key_set", c.AdminAPIKey != "",
		"lexicon_dir", c.LexiconDir,
		"jetstream_url", c.JetstreamURL,
		"jetstream_collections", c.JetstreamCollections,
		"jetstream_disable_cursor", c.JetstreamDisableCursor,
		"backfill_on_start", c.BackfillOnStart,
		"allowed_origins", c.AllowedOrigins,
		"labeler_dids_count", labelerDIDsCount(c.LabelerDIDs),
	)

	if c.AdminAPIKey != "" {
		slog.Info("ADMIN_API_KEY is set: X-User-DID header will be trusted when accompanied by a valid Bearer token")
	}
}

// Address returns the server address in host:port format.
func (c *Config) Address() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// Helper functions

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	intVal, err := strconv.Atoi(value)
	if err != nil {
		// A malformed int env var silently falling back to the
		// default used to make "why is my config ignored?" hard
		// to diagnose. Log at Warn so operators see it on boot.
		slog.Warn("Malformed integer env var; using default",
			"key", key, "value", value, "default", defaultValue, "error", err)
		return defaultValue
	}
	return intVal
}

func getEnvInt64(key string, defaultValue int64) int64 {
	value := os.Getenv(key)
	if value == "" {
		return defaultValue
	}
	intVal, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		slog.Warn("Malformed int64 env var; using default",
			"key", key, "value", value, "default", defaultValue, "error", err)
		return defaultValue
	}
	return intVal
}

func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		lower := strings.ToLower(value)
		return lower == "true" || lower == "1" || lower == "yes"
	}
	return defaultValue
}

func generateRandomKey(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes)[:length], nil
}

// RedactPassword hides the password in a database URL for logging.
// Example: postgres://user:pass@host becomes postgres://user:***@host
func RedactPassword(url string) string {
	if !strings.Contains(url, "@") {
		return url
	}

	parts := strings.SplitN(url, "@", 2)
	if len(parts) != 2 {
		return url
	}

	prefix := parts[0]
	suffix := parts[1]

	if idx := strings.LastIndex(prefix, ":"); idx > 0 {
		if protoIdx := strings.Index(prefix, "://"); protoIdx > 0 && idx > protoIdx {
			return prefix[:idx+1] + "***@" + suffix
		}
	}

	return url
}
