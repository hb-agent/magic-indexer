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
		ExternalBaseURL:   getEnv("EXTERNAL_BASE_URL", ""),
		OAuthSigningKey:   getEnv("OAUTH_SIGNING_KEY", ""),
		OAuthLoopbackMode: getEnvBool("OAUTH_LOOPBACK_MODE", false),

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

	// Set default external base URL if not provided
	if cfg.ExternalBaseURL == "" {
		cfg.ExternalBaseURL = fmt.Sprintf("http://%s:%d", cfg.Host, cfg.Port)
	}

	return cfg, nil
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

	return nil
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
