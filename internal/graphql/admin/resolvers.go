package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/GainForest/hypergoat/internal/atproto"
	didpkg "github.com/GainForest/hypergoat/internal/atproto/did"
	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/lexicon"
	"github.com/GainForest/hypergoat/internal/logsafe"
	"github.com/GainForest/hypergoat/internal/metrics"
	"github.com/GainForest/hypergoat/internal/oauth"
)

// validateOperatorURL verifies that a URL about to be written into
// operator settings is an https URL with a real host. A malicious
// admin could otherwise set relay_url / plc_directory_url to
// http://attacker.local (leaking tokens) or file:///etc/passwd.
// http:// is rejected here rather than merely warned — operators
// wanting a dev-mode override should unset the field.
func validateOperatorURL(field, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid %s: %w", field, err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("invalid %s: scheme must be https, got %q", field, u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid %s: missing host", field)
	}
	return nil
}

// validateJetstreamURL allows ws:// and wss:// in addition to https
// for the Jetstream firehose URL. Jetstream is a websocket
// endpoint so http schemes aren't meaningful.
func validateJetstreamURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid jetstream_url: %w", err)
	}
	switch u.Scheme {
	case "wss", "https":
		// allowed
	case "ws", "http":
		return fmt.Errorf("invalid jetstream_url: scheme must be wss or https, got %q", u.Scheme)
	default:
		return fmt.Errorf("invalid jetstream_url: unsupported scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid jetstream_url: missing host")
	}
	return nil
}

// maxAdminPageSize caps any `first` argument on admin list queries.
// A client asking for millions of rows would otherwise stream them
// all through the resolver and blow up memory on both sides.
const maxAdminPageSize = 200

// clampAdminPageSize normalizes a caller-supplied `first` value to the
// range [1, maxAdminPageSize], defaulting to 20 when zero or negative.
func clampAdminPageSize(first int) int {
	if first <= 0 {
		return 20
	}
	if first > maxAdminPageSize {
		return maxAdminPageSize
	}
	return first
}

// Repositories holds the database repositories needed by the admin API.
type Repositories struct {
	Records          *repositories.RecordsRepository
	Actors           *repositories.ActorsRepository
	Lexicons         *repositories.LexiconsRepository
	Config           *repositories.ConfigRepository
	OAuthClients     *repositories.OAuthClientsRepository
	Activity         *repositories.JetstreamActivityRepository
	Labels           *repositories.LabelsRepository
	LabelDefinitions *repositories.LabelDefinitionsRepository
	LabelPreferences *repositories.LabelPreferencesRepository
	Reports          *repositories.ReportsRepository
}

// BackfillCallback is called when single-actor backfill is triggered.
type BackfillCallback func(ctx context.Context, did string) error

// FullBackfillCallback is called when full network backfill is triggered.
type FullBackfillCallback func(ctx context.Context) error

// LexiconChangeCallback is called when lexicons are added or removed.
type LexiconChangeCallback func(collections []string) error

// SchemaValidateCallback is called before a lexicon upload is committed to
// check whether the proposed set of lexicons (current registry + new/updated
// ones) would produce a valid GraphQL schema. A non-nil error aborts the
// upload — nothing is written to the database. This is the pre-commit guard
// for issue #22: catch schema-breaking uploads before they take down the
// server on restart.
type SchemaValidateCallback func(proposed map[string]string) error

// ProcessRestartCallback is fired when an admin action (currently: successful
// lexicon upload) requires the server to restart to pick up the new schema.
// The implementation is expected to gracefully shut down and then exit the
// process with a non-zero status so the supervising orchestrator (e.g.
// Railway, Docker) can restart it. See issue #22.
type ProcessRestartCallback func(reason string)

// Resolver provides methods for resolving admin GraphQL queries and mutations.
type Resolver struct {
	repos                  *Repositories
	backfillActive         atomic.Bool
	domainDID              string // The DID of this labeler instance
	backfillCallback       BackfillCallback
	fullBackfillCallback   FullBackfillCallback
	lexiconChangeCallback  LexiconChangeCallback
	schemaValidateCallback SchemaValidateCallback
	processRestartCallback ProcessRestartCallback
	// Actor-purge plumbing (Track E). Both are optional — when
	// purgeTokenSigner is nil the preview / confirm mutations
	// return a clear "not configured" error rather than panicking;
	// when tapRemover is nil (TAP_ENABLED=false) the Tap leg of
	// the purge is silently skipped.
	purgeTokenSigner *PurgeTokenSigner
	tapRemover       TapRemover
}

// NewResolver creates a new admin resolver.
func NewResolver(repos *Repositories, domainDID string) *Resolver {
	return &Resolver{
		repos:     repos,
		domainDID: domainDID,
	}
}

// SetBackfillCallback sets the callback for single-actor backfill operations.
func (r *Resolver) SetBackfillCallback(cb BackfillCallback) {
	r.backfillCallback = cb
}

// SetFullBackfillCallback sets the callback for full network backfill operations.
func (r *Resolver) SetFullBackfillCallback(cb FullBackfillCallback) {
	r.fullBackfillCallback = cb
}

// SetLexiconChangeCallback sets the callback for lexicon changes.
func (r *Resolver) SetLexiconChangeCallback(cb LexiconChangeCallback) {
	r.lexiconChangeCallback = cb
}

// SetSchemaValidateCallback sets the pre-commit schema-build check fired
// before a lexicon upload is persisted. See SchemaValidateCallback (issue #22).
func (r *Resolver) SetSchemaValidateCallback(cb SchemaValidateCallback) {
	r.schemaValidateCallback = cb
}

// SetProcessRestartCallback sets the callback that asks the supervising
// orchestrator to restart this process. See ProcessRestartCallback (issue #22).
func (r *Resolver) SetProcessRestartCallback(cb ProcessRestartCallback) {
	r.processRestartCallback = cb
}

// notifyLexiconChange calls the lexicon change callback with current collections.
func (r *Resolver) notifyLexiconChange(ctx context.Context) {
	if r.lexiconChangeCallback == nil {
		return
	}

	lexicons, err := r.repos.Lexicons.GetAll(ctx)
	if err != nil {
		return
	}

	collections := make([]string, len(lexicons))
	for i, lex := range lexicons {
		collections[i] = lex.ID
	}

	if err := r.lexiconChangeCallback(collections); err != nil {
		// Log but don't fail the operation
		slog.Warn("Failed to notify lexicon change", "error", err)
	}
}

// =============================================================================
// Query Resolvers
// =============================================================================

// Statistics returns system statistics.
func (r *Resolver) Statistics(ctx context.Context) (map[string]interface{}, error) {
	recordCount, err := r.repos.Records.GetCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get record count: %w", err)
	}

	actorCount, err := r.repos.Actors.GetCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get actor count: %w", err)
	}

	lexiconCount, err := r.repos.Lexicons.GetCount(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get lexicon count: %w", err)
	}

	return map[string]interface{}{
		"recordCount":  recordCount,
		"actorCount":   actorCount,
		"lexiconCount": lexiconCount,
	}, nil
}

// CurrentSession returns the current user's session info.
func (r *Resolver) CurrentSession(ctx context.Context, userDID, handle string, adminDIDs []string) map[string]interface{} {
	isAdmin := false
	for _, adminDID := range adminDIDs {
		if adminDID == userDID {
			isAdmin = true
			break
		}
	}

	return map[string]interface{}{
		"did":     userDID,
		"handle":  handle,
		"isAdmin": isAdmin,
	}
}

// Settings returns system settings.
func (r *Resolver) Settings(ctx context.Context) (map[string]interface{}, error) {
	domainAuthority, _ := r.repos.Config.Get(ctx, "domain_authority")
	adminDidsStr, _ := r.repos.Config.Get(ctx, "admin_dids")
	relayURL, _ := r.repos.Config.Get(ctx, "relay_url")
	plcDirectoryURL, _ := r.repos.Config.Get(ctx, "plc_directory_url")
	jetstreamURL, _ := r.repos.Config.Get(ctx, "jetstream_url")
	oauthScopes, _ := r.repos.Config.Get(ctx, "oauth_supported_scopes")

	// Parse admin DIDs from comma-separated string
	var adminDids []string
	if adminDidsStr != "" {
		adminDids = strings.Split(adminDidsStr, ",")
		for i := range adminDids {
			adminDids[i] = strings.TrimSpace(adminDids[i])
		}
	}

	return map[string]interface{}{
		"id":                   "settings",
		"domainAuthority":      domainAuthority,
		"adminDids":            adminDids,
		"relayUrl":             relayURL,
		"plcDirectoryUrl":      plcDirectoryURL,
		"jetstreamUrl":         jetstreamURL,
		"oauthSupportedScopes": oauthScopes,
	}, nil
}

// IsBackfilling returns whether a backfill is currently active.
func (r *Resolver) IsBackfilling() bool {
	return r.backfillActive.Load()
}

// SetBackfillActive sets the backfill status.
func (r *Resolver) SetBackfillActive(active bool) {
	r.backfillActive.Store(active)
}

// Lexicons returns all lexicon definitions.
func (r *Resolver) Lexicons(ctx context.Context) ([]map[string]interface{}, error) {
	lexicons, err := r.repos.Lexicons.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get lexicons: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(lexicons))
	for _, lex := range lexicons {
		result = append(result, map[string]interface{}{
			"id":        lex.ID,
			"json":      lex.JSON,
			"createdAt": lex.CreatedAt.Format(time.RFC3339),
		})
	}

	return result, nil
}

// OAuthClients returns all OAuth client registrations.
func (r *Resolver) OAuthClients(ctx context.Context) ([]map[string]interface{}, error) {
	clients, err := r.repos.OAuthClients.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get OAuth clients: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(clients))
	for _, client := range clients {
		result = append(result, map[string]interface{}{
			"clientId":     client.ClientID,
			"clientSecret": client.ClientSecret,
			"clientName":   client.ClientName,
			"clientType":   string(client.ClientType),
			"redirectUris": client.RedirectURIs,
			"scope":        client.Scope,
			"createdAt":    client.CreatedAt,
		})
	}

	return result, nil
}

// Upload size limits for lexicon ZIP files.
const (
	maxLexiconUploadBytes = 10 * 1024 * 1024 // 10MB max ZIP size
	maxLexiconFileCount   = 500              // Max files in ZIP
	maxLexiconFileSize    = 1 * 1024 * 1024  // 1MB max per file
)

// UploadLexicons extracts lexicons from a base64-encoded ZIP file.
func (r *Resolver) UploadLexicons(ctx context.Context, zipBase64 string) (int, error) {
	// Validate base64 input size before decoding (base64 encodes 3 bytes as 4 chars)
	maxBase64Len := maxLexiconUploadBytes * 4 / 3
	if len(zipBase64) > maxBase64Len {
		return 0, fmt.Errorf("upload too large: estimated %d bytes exceeds %d byte limit",
			len(zipBase64)*3/4, maxLexiconUploadBytes)
	}

	// Decode base64
	zipData, err := base64.StdEncoding.DecodeString(zipBase64)
	if err != nil {
		return 0, fmt.Errorf("invalid base64 data: %w", err)
	}

	// Open ZIP reader
	zipReader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return 0, fmt.Errorf("invalid ZIP file: %w", err)
	}

	// Check file count to prevent zip bombs
	if len(zipReader.File) > maxLexiconFileCount {
		return 0, fmt.Errorf("too many files in ZIP: %d exceeds limit of %d",
			len(zipReader.File), maxLexiconFileCount)
	}

	// Stage 1: parse every entry into memory (id -> json). We can't upsert
	// as we go any more — issue #22 requires validating that the resulting
	// schema builds before any DB writes land.
	proposed := make(map[string]string, len(zipReader.File))
	for _, file := range zipReader.File {
		if file.FileInfo().IsDir() || !strings.HasSuffix(file.Name, ".json") {
			continue
		}
		if file.UncompressedSize64 > maxLexiconFileSize {
			return 0, fmt.Errorf("file %s too large: %d bytes exceeds %d byte limit",
				file.Name, file.UncompressedSize64, maxLexiconFileSize)
		}
		rc, err := file.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxLexiconFileSize+1))
		_ = rc.Close()
		if err != nil {
			continue
		}
		if len(data) > maxLexiconFileSize {
			return 0, fmt.Errorf("file %s exceeds %d byte limit after decompression",
				file.Name, maxLexiconFileSize)
		}

		var lexEntry struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(data, &lexEntry); err != nil {
			continue
		}
		if lexEntry.ID == "" {
			continue
		}
		proposed[lexEntry.ID] = string(data)
	}

	if len(proposed) == 0 {
		return 0, nil
	}

	// Stage 2: pre-commit schema validation (issue #22). If the proposed set
	// wouldn't produce a valid GraphQL schema, reject the whole upload so
	// the server stays up on its current schema.
	if r.schemaValidateCallback != nil {
		if err := r.schemaValidateCallback(proposed); err != nil {
			return 0, fmt.Errorf("lexicon upload rejected: schema validation failed: %w", err)
		}
	}

	// Stage 3: commit. Validation already passed; upsert failures here are
	// DB errors and leave the upload partially applied — caller sees how
	// many were saved before the failure.
	count := 0
	for id, body := range proposed {
		if err := r.repos.Lexicons.Upsert(ctx, id, body); err != nil {
			return count, fmt.Errorf("failed to save lexicon %s: %w", id, err)
		}
		count++
	}

	// Notify Jetstream consumer of collection changes.
	if count > 0 {
		r.notifyLexiconChange(ctx)
	}

	// Restart so the new schema is picked up on boot (issue #22). Fired
	// after notifyLexiconChange so any synchronous work it kicks off still
	// runs, but the orchestrator takes over from here.
	if count > 0 && r.processRestartCallback != nil {
		r.processRestartCallback(fmt.Sprintf("lexicon upload applied %d lexicon(s); restarting to rebuild schema", count))
	}

	return count, nil
}

// TriggerBackfill starts a full backfill process.
// Uses atomic CompareAndSwap to prevent concurrent backfill launches (race-safe).
func (r *Resolver) TriggerBackfill(ctx context.Context) (bool, error) {
	if r.fullBackfillCallback == nil {
		return false, fmt.Errorf("full backfill not configured")
	}

	// Atomically check-and-set to prevent concurrent backfill launches
	if !r.backfillActive.CompareAndSwap(false, true) {
		return false, fmt.Errorf("backfill already in progress")
	}

	// Run backfill in background goroutine
	go func() {
		defer r.backfillActive.Store(false)

		// Use background context since HTTP request context will be cancelled
		if err := r.fullBackfillCallback(context.Background()); err != nil {
			slog.Error("[backfill] Full backfill failed in background", "error", err)
			return
		}
	}()

	return true, nil
}

// BackfillActor queues a single actor for backfill.
func (r *Resolver) BackfillActor(ctx context.Context, did string) (bool, error) {
	// Strict DID validation: prefix-only checks let newline / control-char
	// payloads into the actor table and log lines (commit c069afa).
	if !didpkg.IsValid(did) {
		return false, fmt.Errorf("invalid DID")
	}

	// Ensure actor exists (creates if not)
	if err := r.repos.Actors.Upsert(ctx, did, ""); err != nil {
		return false, fmt.Errorf("failed to register actor: %w", err)
	}

	// Trigger backfill callback if registered
	if r.backfillCallback != nil {
		if err := r.backfillCallback(ctx, did); err != nil {
			return false, fmt.Errorf("failed to trigger backfill: %w", err)
		}
	}

	return true, nil
}

// CreateOAuthClient creates a new OAuth client.
func (r *Resolver) CreateOAuthClient(ctx context.Context, clientName, clientType string, redirectURIs []string) (map[string]interface{}, error) {
	// Generate client ID
	clientIDBytes := make([]byte, 16)
	if _, err := rand.Read(clientIDBytes); err != nil {
		return nil, fmt.Errorf("failed to generate client ID: %w", err)
	}
	clientID := hex.EncodeToString(clientIDBytes)

	// Generate client secret for confidential clients
	var clientSecret *string
	ct := oauth.ClientType(clientType)
	if ct == oauth.ClientConfidential {
		secretBytes := make([]byte, 32)
		if _, err := rand.Read(secretBytes); err != nil {
			return nil, fmt.Errorf("failed to generate client secret: %w", err)
		}
		secret := hex.EncodeToString(secretBytes)
		clientSecret = &secret
	}

	now := time.Now().Unix()
	client := &oauth.Client{
		ClientID:                clientID,
		ClientSecret:            clientSecret,
		ClientName:              clientName,
		RedirectURIs:            redirectURIs,
		GrantTypes:              []oauth.GrantType{oauth.GrantAuthorizationCode, oauth.GrantRefreshToken},
		ResponseTypes:           []oauth.ResponseType{oauth.ResponseCode},
		TokenEndpointAuthMethod: oauth.AuthClientSecret,
		ClientType:              ct,
		CreatedAt:               now,
		UpdatedAt:               now,
		Metadata:                "{}",
		AccessTokenExpiration:   3600,       // 1 hour
		RefreshTokenExpiration:  86400 * 30, // 30 days
		RequireRedirectExact:    true,
	}

	if ct == oauth.ClientPublic {
		client.TokenEndpointAuthMethod = oauth.AuthNone
	}

	if err := r.repos.OAuthClients.Insert(ctx, client); err != nil {
		return nil, fmt.Errorf("failed to create OAuth client: %w", err)
	}

	result := map[string]interface{}{
		"clientId":     client.ClientID,
		"clientName":   client.ClientName,
		"clientType":   string(client.ClientType),
		"redirectUris": client.RedirectURIs,
		"createdAt":    client.CreatedAt,
		"scope":        client.Scope,
	}
	if client.ClientSecret != nil {
		result["clientSecret"] = *client.ClientSecret
	}

	return result, nil
}

// UpdateOAuthClient updates an existing OAuth client.
func (r *Resolver) UpdateOAuthClient(ctx context.Context, clientID, clientName string, redirectURIs []string) (map[string]interface{}, error) {
	// Get existing client
	client, err := r.repos.OAuthClients.Get(ctx, clientID)
	if err != nil {
		return nil, fmt.Errorf("failed to get OAuth client: %w", err)
	}
	if client == nil {
		return nil, fmt.Errorf("OAuth client not found")
	}

	// Update fields
	client.ClientName = clientName
	client.RedirectURIs = redirectURIs
	client.UpdatedAt = time.Now().Unix()

	if err := r.repos.OAuthClients.Update(ctx, client); err != nil {
		return nil, fmt.Errorf("failed to update OAuth client: %w", err)
	}

	result := map[string]interface{}{
		"clientId":     client.ClientID,
		"clientName":   client.ClientName,
		"clientType":   string(client.ClientType),
		"redirectUris": client.RedirectURIs,
		"createdAt":    client.CreatedAt,
		"scope":        client.Scope,
	}
	if client.ClientSecret != nil {
		result["clientSecret"] = *client.ClientSecret
	}

	return result, nil
}

// DeleteOAuthClient deletes an OAuth client.
func (r *Resolver) DeleteOAuthClient(ctx context.Context, clientID string) (bool, error) {
	// Don't allow deleting the admin client
	if clientID == "admin" {
		return false, fmt.Errorf("cannot delete the admin client")
	}

	if err := r.repos.OAuthClients.Delete(ctx, clientID); err != nil {
		return false, fmt.Errorf("failed to delete OAuth client: %w", err)
	}

	return true, nil
}

// AddAdmin adds a DID to the admin list.
func (r *Resolver) AddAdmin(ctx context.Context, did string) (bool, error) {
	// Strict DID validation: admin_dids is a CSV; a newline-bearing DID
	// would silently grant admin to a forged second entry on the next
	// read. Matches the discipline enforced by UpdateSettings (#64,
	// commit c069afa).
	if !didpkg.IsValid(did) {
		return false, fmt.Errorf("invalid DID")
	}

	// Get current admin DIDs
	adminDidsStr, _ := r.repos.Config.Get(ctx, "admin_dids")
	var adminDids []string
	if adminDidsStr != "" {
		adminDids = strings.Split(adminDidsStr, ",")
		for i := range adminDids {
			adminDids[i] = strings.TrimSpace(adminDids[i])
		}
	}

	// Check if already admin
	for _, existingDID := range adminDids {
		if existingDID == did {
			return true, nil // Already an admin
		}
	}

	// Add new admin
	adminDids = append(adminDids, did)
	newAdminDidsStr := strings.Join(adminDids, ",")

	if err := r.repos.Config.Set(ctx, "admin_dids", newAdminDidsStr); err != nil {
		return false, fmt.Errorf("failed to update admin_dids: %w", err)
	}

	// Audit log + metric. target_did is the DID being granted
	// admin; actor_did is the admin performing the grant. Both
	// flow through logsafe.DID — defense-in-depth even though the
	// upstream did.IsValid checks already validated them. The
	// field label matches UpdateSettings(admin_dids) so the same
	// dashboard counter captures every path that mutates the
	// admin-DID set.
	actorDID, _ := ctx.Value(contextKeyUserDID).(string)
	slog.Info("admin added",
		"event", "admin_added",
		"actor_did", logsafe.DID(actorDID),
		"target_did", logsafe.DID(did),
		"total_admins", len(adminDids),
		"ts", time.Now().UTC().Format(time.RFC3339),
	)
	metrics.AdminSettingsChanged(metrics.AdminSettingsFieldAdminDIDs)

	return true, nil
}

// RemoveAdmin removes a DID from the admin list.
func (r *Resolver) RemoveAdmin(ctx context.Context, did string) (bool, error) {
	// Strict DID validation on the input even though the function is
	// only removing — a malformed input would otherwise produce a
	// confusing "DID is not an admin" error for a value that was never
	// a valid DID, and would log a forged-shape value into the audit
	// trail (track 10 wires audit logs here).
	if !didpkg.IsValid(did) {
		return false, fmt.Errorf("invalid DID")
	}

	// Get current admin DIDs
	adminDidsStr, _ := r.repos.Config.Get(ctx, "admin_dids")
	if adminDidsStr == "" {
		return false, fmt.Errorf("no admins configured")
	}

	adminDids := strings.Split(adminDidsStr, ",")
	for i := range adminDids {
		adminDids[i] = strings.TrimSpace(adminDids[i])
	}

	// Prevent removing the last admin
	if len(adminDids) <= 1 {
		return false, fmt.Errorf("cannot remove the last admin")
	}

	// Find and remove the DID
	found := false
	newAdminDids := make([]string, 0, len(adminDids)-1)
	for _, existingDID := range adminDids {
		if existingDID == did {
			found = true
		} else {
			newAdminDids = append(newAdminDids, existingDID)
		}
	}

	if !found {
		return false, fmt.Errorf("DID is not an admin")
	}

	newAdminDidsStr := strings.Join(newAdminDids, ",")
	if err := r.repos.Config.Set(ctx, "admin_dids", newAdminDidsStr); err != nil {
		return false, fmt.Errorf("failed to update admin_dids: %w", err)
	}

	// Audit log + metric. Shape mirrors admin_added so log
	// shippers can route both events with one rule. total_admins
	// is the count *after* removal (== len(newAdminDids)) so an
	// operator scanning the line knows the resulting admin set
	// size.
	actorDID, _ := ctx.Value(contextKeyUserDID).(string)
	slog.Info("admin removed",
		"event", "admin_removed",
		"actor_did", logsafe.DID(actorDID),
		"target_did", logsafe.DID(did),
		"total_admins", len(newAdminDids),
		"ts", time.Now().UTC().Format(time.RFC3339),
	)
	metrics.AdminSettingsChanged(metrics.AdminSettingsFieldAdminDIDs)

	return true, nil
}

// RegisterLexicon resolves an NSID via DNS and registers the lexicon schema.
func (r *Resolver) RegisterLexicon(ctx context.Context, nsid string) (map[string]interface{}, error) {
	// Validate NSID format (at least 3 dot-separated segments)
	parts := strings.Split(nsid, ".")
	if len(parts) < 3 {
		return nil, fmt.Errorf("invalid NSID format: must have at least 3 segments (e.g., app.bsky.feed.post)")
	}

	// Check if lexicon already exists
	exists, err := r.repos.Lexicons.Exists(ctx, nsid)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing lexicon: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("lexicon %s is already registered", nsid)
	}

	// Resolve lexicon via DNS and PDS
	resolver := lexicon.NewResolver()
	resolved, err := resolver.ResolveLexicon(ctx, nsid)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve lexicon: %w", err)
	}

	// Store the lexicon schema
	schemaJSON := string(resolved.Schema)
	if err := r.repos.Lexicons.Upsert(ctx, nsid, schemaJSON); err != nil {
		return nil, fmt.Errorf("failed to save lexicon: %w", err)
	}

	// Notify Jetstream consumer of collection changes
	r.notifyLexiconChange(ctx)

	// Parse schema to extract description
	var schema struct {
		ID          string `json:"id"`
		Description string `json:"description"`
		Defs        map[string]struct {
			Description string `json:"description"`
		} `json:"defs"`
	}
	_ = json.Unmarshal(resolved.Schema, &schema)

	description := schema.Description
	if description == "" && schema.Defs != nil {
		if main, ok := schema.Defs["main"]; ok {
			description = main.Description
		}
	}

	return map[string]interface{}{
		"id":          nsid,
		"json":        schemaJSON,
		"createdAt":   time.Now().Format(time.RFC3339),
		"did":         resolved.DID,
		"description": description,
	}, nil
}

// DeleteLexicon removes a registered lexicon by NSID.
func (r *Resolver) DeleteLexicon(ctx context.Context, nsid string) (bool, error) {
	exists, err := r.repos.Lexicons.Exists(ctx, nsid)
	if err != nil {
		return false, fmt.Errorf("failed to check lexicon: %w", err)
	}
	if !exists {
		return false, fmt.Errorf("lexicon %s not found", nsid)
	}

	if err := r.repos.Lexicons.Delete(ctx, nsid); err != nil {
		return false, fmt.Errorf("failed to delete lexicon: %w", err)
	}

	// Notify Jetstream consumer of collection changes
	r.notifyLexiconChange(ctx)

	return true, nil
}

// CreateFieldIndex creates a partial expression index on a JSON field for a collection.
func (r *Resolver) CreateFieldIndex(ctx context.Context, collection, field string) (map[string]interface{}, error) {
	idxName, err := r.repos.Records.CreateFieldIndex(ctx, collection, field)
	if err != nil {
		return map[string]interface{}{"success": false, "indexName": ""}, err
	}
	return map[string]interface{}{"success": true, "indexName": idxName}, nil
}

// DropFieldIndex drops a previously created field expression index.
func (r *Resolver) DropFieldIndex(ctx context.Context, collection, field string) (bool, error) {
	if err := r.repos.Records.DropFieldIndex(ctx, collection, field); err != nil {
		return false, err
	}
	return true, nil
}

// ActivityBuckets returns aggregated activity data for the specified time range.
func (r *Resolver) ActivityBuckets(ctx context.Context, timeRange string) ([]map[string]interface{}, error) {
	buckets, err := r.repos.Activity.GetActivityBuckets(ctx, timeRange)
	if err != nil {
		return nil, fmt.Errorf("failed to get activity buckets: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(buckets))
	for _, bucket := range buckets {
		result = append(result, map[string]interface{}{
			"timestamp": bucket.Timestamp.Format(time.RFC3339),
			"total":     bucket.Total,
			"creates":   bucket.Creates,
			"updates":   bucket.Updates,
			"deletes":   bucket.Deletes,
		})
	}

	return result, nil
}

// CollectionOverview returns per-collection record counts with invalid counts.
func (r *Resolver) CollectionOverview(ctx context.Context) ([]map[string]interface{}, error) {
	overview, err := r.repos.Records.GetCollectionOverview(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get collection overview: %w", err)
	}
	result := make([]map[string]interface{}, 0, len(overview))
	for _, c := range overview {
		result = append(result, map[string]interface{}{
			"collection":   c.Collection,
			"recordCount":  c.RecordCount,
			"invalidCount": c.InvalidCount,
		})
	}
	return result, nil
}

// RecentActivity returns recent activity entries.
func (r *Resolver) RecentActivity(ctx context.Context, hours int) ([]map[string]interface{}, error) {
	entries, err := r.repos.Activity.GetRecentActivity(ctx, hours)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent activity: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(entries))
	for _, entry := range entries {
		item := map[string]interface{}{
			"id":         entry.ID,
			"timestamp":  entry.Timestamp.Format(time.RFC3339),
			"operation":  entry.Operation,
			"collection": entry.Collection,
			"did":        entry.DID,
			"status":     entry.Status,
			"eventJson":  entry.EventJSON,
		}
		if entry.RKey != nil {
			item["rkey"] = *entry.RKey
		}
		if entry.ErrorMessage != nil {
			item["errorMessage"] = *entry.ErrorMessage
		}
		if entry.IsValid != nil {
			item["isValid"] = *entry.IsValid
		}
		result = append(result, item)
	}

	return result, nil
}

// ValidationStats returns aggregated validation statistics for the specified time range.
func (r *Resolver) ValidationStats(ctx context.Context, timeRange string) (map[string]interface{}, error) {
	stats, err := r.repos.Activity.GetValidationStats(ctx, timeRange)
	if err != nil {
		return nil, fmt.Errorf("failed to get validation stats: %w", err)
	}

	// Get recent invalid entries
	recentInvalid, err := r.repos.Activity.GetRecentInvalidActivity(ctx, 20)
	if err != nil {
		return nil, fmt.Errorf("failed to get recent invalid activity: %w", err)
	}

	// Map recent invalid to GraphQL format
	recentItems := make([]map[string]interface{}, 0, len(recentInvalid))
	for _, entry := range recentInvalid {
		item := map[string]interface{}{
			"id":         entry.ID,
			"timestamp":  entry.Timestamp.Format(time.RFC3339),
			"operation":  entry.Operation,
			"collection": entry.Collection,
			"did":        entry.DID,
			"status":     entry.Status,
			"eventJson":  entry.EventJSON,
		}
		if entry.RKey != nil {
			item["rkey"] = *entry.RKey
		}
		if entry.ErrorMessage != nil {
			item["errorMessage"] = *entry.ErrorMessage
		}
		if entry.IsValid != nil {
			item["isValid"] = *entry.IsValid
		}
		recentItems = append(recentItems, item)
	}

	// Map invalidByCollection
	byCollection := make([]map[string]interface{}, 0, len(stats.InvalidByCollection))
	for _, c := range stats.InvalidByCollection {
		byCollection = append(byCollection, map[string]interface{}{
			"collection": c.Collection,
			"count":      c.Count,
		})
	}

	result := map[string]interface{}{
		"invalidCount":        stats.InvalidCount,
		"invalidByCollection": byCollection,
		"recentInvalid":       recentItems,
	}
	if stats.LastInvalidAt != nil {
		result["lastInvalidAt"] = stats.LastInvalidAt.Format(time.RFC3339)
	}

	return result, nil
}

// LabelDefinitions returns all label definitions. Each entry now
// includes the owning labeler's `src` DID (the pre-seeded Bluesky
// defaults live under repositories.SystemLabelerSrc).
func (r *Resolver) LabelDefinitions(ctx context.Context) ([]map[string]interface{}, error) {
	defs, err := r.repos.LabelDefinitions.GetAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get label definitions: %w", err)
	}

	result := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		result = append(result, map[string]interface{}{
			"src":               def.Src,
			"val":               def.Val,
			"description":       def.Description,
			"severity":          string(def.Severity),
			"defaultVisibility": string(def.DefaultVisibility),
			"createdAt":         def.CreatedAt.Format(time.RFC3339),
		})
	}

	return result, nil
}

// ViewerLabelPreferences returns the current user's label
// preferences, joined against the non-system label definitions. A
// user can override visibility independently per (labeler, val) tuple.
func (r *Resolver) ViewerLabelPreferences(ctx context.Context, userDID string) ([]map[string]interface{}, error) {
	// Get all non-system label definitions (both per-labeler and
	// legacy rows that don't belong to an external labeler).
	defs, err := r.repos.LabelDefinitions.GetNonSystem(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get label definitions: %w", err)
	}

	// Get user preferences across every labeler.
	prefs, err := r.repos.LabelPreferences.GetByDID(ctx, userDID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user preferences: %w", err)
	}

	// Build preference map keyed by (src, val) for quick lookup.
	type prefKey struct{ src, val string }
	prefMap := make(map[prefKey]repositories.LabelVisibility)
	for _, pref := range prefs {
		prefMap[prefKey{pref.Src, pref.LabelVal}] = pref.Visibility
	}

	// Build result with effective visibility
	result := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		visibility := def.DefaultVisibility
		if userVis, ok := prefMap[prefKey{def.Src, def.Val}]; ok {
			visibility = userVis
		}

		result = append(result, map[string]interface{}{
			"src":               def.Src,
			"val":               def.Val,
			"description":       def.Description,
			"severity":          string(def.Severity),
			"defaultVisibility": string(def.DefaultVisibility),
			"visibility":        string(visibility),
		})
	}

	return result, nil
}

// Labels returns labels with optional filters and pagination.
func (r *Resolver) Labels(ctx context.Context, uriFilter, valFilter *string, first int, after *string) (map[string]interface{}, error) {
	first = clampAdminPageSize(first)

	// Decode cursor to get afterID
	var afterID *int64
	if after != nil && *after != "" {
		decoded, err := base64.URLEncoding.DecodeString(*after)
		if err == nil {
			if id, err := strconv.ParseInt(string(decoded), 10, 64); err == nil {
				afterID = &id
			}
		}
	}

	paginated, err := r.repos.Labels.GetPaginated(ctx, uriFilter, valFilter, first, afterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get labels: %w", err)
	}

	edges := make([]map[string]interface{}, 0, len(paginated.Labels))
	var startCursor, endCursor string

	for _, label := range paginated.Labels {
		cursor := base64.URLEncoding.EncodeToString([]byte(strconv.FormatInt(label.ID, 10)))
		if startCursor == "" {
			startCursor = cursor
		}
		endCursor = cursor

		node := map[string]interface{}{
			"id":  label.ID,
			"src": label.Src,
			"uri": label.URI,
			"val": label.Val,
			"neg": label.Neg,
			"cts": label.Cts.Format(time.RFC3339),
		}
		if label.CID != nil {
			node["cid"] = *label.CID
		}
		if label.Exp != nil {
			node["exp"] = label.Exp.Format(time.RFC3339)
		}

		edges = append(edges, map[string]interface{}{
			"cursor": cursor,
			"node":   node,
		})
	}

	return map[string]interface{}{
		"edges": edges,
		"pageInfo": map[string]interface{}{
			"hasNextPage":     paginated.HasNextPage,
			"hasPreviousPage": after != nil && *after != "",
			"startCursor":     startCursor,
			"endCursor":       endCursor,
		},
		"totalCount": paginated.TotalCount,
	}, nil
}

// Reports returns reports with optional status filter and pagination.
func (r *Resolver) Reports(ctx context.Context, statusFilter *string, first int, after *string) (map[string]interface{}, error) {
	first = clampAdminPageSize(first)

	// Convert status filter
	var status *repositories.ReportStatus
	if statusFilter != nil {
		s := repositories.ReportStatus(*statusFilter)
		status = &s
	}

	// Decode cursor to get afterID
	var afterID *int64
	if after != nil && *after != "" {
		decoded, err := base64.URLEncoding.DecodeString(*after)
		if err == nil {
			if id, err := strconv.ParseInt(string(decoded), 10, 64); err == nil {
				afterID = &id
			}
		}
	}

	paginated, err := r.repos.Reports.GetPaginated(ctx, status, first, afterID)
	if err != nil {
		return nil, fmt.Errorf("failed to get reports: %w", err)
	}

	edges := make([]map[string]interface{}, 0, len(paginated.Reports))
	var startCursor, endCursor string

	for _, report := range paginated.Reports {
		cursor := base64.URLEncoding.EncodeToString([]byte(strconv.FormatInt(report.ID, 10)))
		if startCursor == "" {
			startCursor = cursor
		}
		endCursor = cursor

		node := map[string]interface{}{
			"id":          report.ID,
			"reporterDid": report.ReporterDID,
			"subjectUri":  report.SubjectURI,
			"reasonType":  string(report.ReasonType),
			"status":      string(report.Status),
			"createdAt":   report.CreatedAt.Format(time.RFC3339),
		}
		if report.Reason != nil {
			node["reason"] = *report.Reason
		}
		if report.ResolvedBy != nil {
			node["resolvedBy"] = *report.ResolvedBy
		}
		if report.ResolvedAt != nil {
			node["resolvedAt"] = report.ResolvedAt.Format(time.RFC3339)
		}

		edges = append(edges, map[string]interface{}{
			"cursor": cursor,
			"node":   node,
		})
	}

	return map[string]interface{}{
		"edges": edges,
		"pageInfo": map[string]interface{}{
			"hasNextPage":     paginated.HasNextPage,
			"hasPreviousPage": after != nil && *after != "",
			"startCursor":     startCursor,
			"endCursor":       endCursor,
		},
		"totalCount": paginated.TotalCount,
	}, nil
}

// =============================================================================
// Mutation Resolvers
// =============================================================================

// UpdateSettings updates system settings. Every applied change
// emits a structured audit log line (`event=admin_settings_changed
// field=<name> before=… after=… actor_did=…`) and increments
// hypergoat_admin_settings_changed_total{field=<name>}. Both `before`
// and `after` go through logsafe.String — they are operator-supplied
// URLs and free-form strings that must not forge log lines.
//
// Only fields actually applied (after validation) are audited; a
// validation rejection returns early and the metric stays flat.
func (r *Resolver) UpdateSettings(ctx context.Context, domainAuthority, adminDids, relayURL, plcDirectoryURL, jetstreamURL, oauthScopes *string) (map[string]interface{}, error) {
	// adminDID is the calling admin — set by the auth middleware
	// after did.IsValid. Used as actor_did on the audit line.
	// Missing context value collapses to "" which logsafe.DID
	// renders as the invalid-did sentinel, so a future bug that
	// bypasses the middleware still produces a forensically
	// usable log line.
	actorDID, _ := ctx.Value(contextKeyUserDID).(string)

	if domainAuthority != nil {
		before, _ := r.repos.Config.Get(ctx, "domain_authority")
		if err := r.repos.Config.Set(ctx, "domain_authority", *domainAuthority); err != nil {
			return nil, fmt.Errorf("failed to update domain_authority: %w", err)
		}
		auditSettingsChanged(actorDID, metrics.AdminSettingsFieldDomainAuthority, before, *domainAuthority)
	}

	if adminDids != nil {
		// adminDids is passed as comma-separated string. Validate
		// each entry so an admin can't lock the instance out with
		// a typo, and so log injection via embedded newlines is
		// impossible.
		for _, raw := range strings.Split(*adminDids, ",") {
			d := strings.TrimSpace(raw)
			if d == "" {
				continue
			}
			if !didpkg.IsValid(d) {
				return nil, fmt.Errorf("invalid admin DID: %q", d)
			}
		}
		before, _ := r.repos.Config.Get(ctx, "admin_dids")
		if err := r.repos.Config.Set(ctx, "admin_dids", *adminDids); err != nil {
			return nil, fmt.Errorf("failed to update admin_dids: %w", err)
		}
		auditSettingsChanged(actorDID, metrics.AdminSettingsFieldAdminDIDs, before, *adminDids)
	}

	if relayURL != nil {
		if err := validateOperatorURL("relay_url", *relayURL); err != nil {
			return nil, err
		}
		before, _ := r.repos.Config.Get(ctx, "relay_url")
		if err := r.repos.Config.Set(ctx, "relay_url", *relayURL); err != nil {
			return nil, fmt.Errorf("failed to update relay_url: %w", err)
		}
		auditSettingsChanged(actorDID, metrics.AdminSettingsFieldRelayURL, before, *relayURL)
	}

	if plcDirectoryURL != nil {
		if err := validateOperatorURL("plc_directory_url", *plcDirectoryURL); err != nil {
			return nil, err
		}
		before, _ := r.repos.Config.Get(ctx, "plc_directory_url")
		if err := r.repos.Config.Set(ctx, "plc_directory_url", *plcDirectoryURL); err != nil {
			return nil, fmt.Errorf("failed to update plc_directory_url: %w", err)
		}
		auditSettingsChanged(actorDID, metrics.AdminSettingsFieldPLCDirectoryURL, before, *plcDirectoryURL)
	}

	if jetstreamURL != nil {
		if err := validateJetstreamURL(*jetstreamURL); err != nil {
			return nil, err
		}
		before, _ := r.repos.Config.Get(ctx, "jetstream_url")
		if err := r.repos.Config.Set(ctx, "jetstream_url", *jetstreamURL); err != nil {
			return nil, fmt.Errorf("failed to update jetstream_url: %w", err)
		}
		auditSettingsChanged(actorDID, metrics.AdminSettingsFieldJetstreamURL, before, *jetstreamURL)
	}

	if oauthScopes != nil {
		before, _ := r.repos.Config.Get(ctx, "oauth_supported_scopes")
		if err := r.repos.Config.Set(ctx, "oauth_supported_scopes", *oauthScopes); err != nil {
			return nil, fmt.Errorf("failed to update oauth_supported_scopes: %w", err)
		}
		auditSettingsChanged(actorDID, metrics.AdminSettingsFieldOAuthSupportedScopes, before, *oauthScopes)
	}

	return r.Settings(ctx)
}

// auditSettingsChanged emits the structured per-field audit log
// line and increments the matching metric. Centralised here so
// every UpdateSettings branch (and AddAdmin / RemoveAdmin) renders
// the same shape — log aggregators can route on a single
// `event=admin_settings_changed` rule.
//
// `before` and `after` are operator-controlled strings (URLs,
// DID lists) and MUST go through logsafe.String. actor_did goes
// through logsafe.DID — the auth middleware already validated it
// with did.IsValid; this is belt-and-braces.
func auditSettingsChanged(actorDID, field, before, after string) {
	slog.Info("admin settings changed",
		"event", "admin_settings_changed",
		"actor_did", logsafe.DID(actorDID),
		"field", field,
		"before", logsafe.String(before),
		"after", logsafe.String(after),
		"ts", time.Now().UTC().Format(time.RFC3339),
	)
	metrics.AdminSettingsChanged(field)
}

// resetAllTables is the hard-listed deletion target set for the
// resetAll admin mutation. ORDER MATTERS — child rows go before
// parents so FK constraints don't reject the delete. Tables NOT in
// this list are preserved intentionally:
//
//   - schema_migrations: bookkeeping, must outlive a reset.
//   - config: operator settings (admin_dids, relay_url, ...) — a
//     reset must not lock the operator out of their own instance.
//   - lexicon: schema definitions. The point of resetAll is to wipe
//     data, not unregister lexicons.
//   - label_definition: includes the seeded Bluesky takedown
//     vocabulary; preserved by design.
//   - oauth_client: registered client apps. A reset invalidates
//     every issued token (below) but keeps the registrations so
//     existing apps can re-authenticate.
//   - jetstream_cursor: operational state. Wiping this would force
//     a re-backfill from the relay's earliest cursor.
//
// SOURCE OF TRUTH: the migration files in
// internal/database/migrations/postgres/*.up.sql. When a new
// migration adds a table whose contents are user/actor/activity
// data, append it to this list. TODO(track-3 follow-up): when this
// list outgrows ~30 entries, replace with the introspection
// approach (SELECT FROM pg_tables WHERE schemaname='public') so it
// can't rot quietly.
var resetAllTables = []string{
	// Notifications subsystem (migration 015). notification_participant
	// has a FK to notification; child first.
	"notification_participant",
	"notification",
	"actor_state",

	// Moderation: reports + applied labels + per-user prefs
	// (migrations 003, 004). label_definition is preserved.
	"actor_label_preference",
	"label",
	"report",

	// OAuth tokens / sessions / replay caches / requests
	// (migration 001 + 016). oauth_client is preserved so
	// registered apps can re-authenticate. All token / session
	// tables FK to oauth_client with ON DELETE CASCADE; we
	// delete them explicitly to make the count exact.
	"oauth_authorization_code",
	"oauth_atp_request",
	"oauth_atp_session",
	"oauth_auth_request",
	"oauth_dpop_jti",
	"oauth_dpop_nonce",
	"oauth_par_request",
	"oauth_refresh_token",
	"oauth_access_token",
	"admin_session",

	// Activity log (migration 001).
	"jetstream_activity",

	// Records authored by every actor (migration 001).
	"record",

	// Actors themselves (migration 001).
	"actor",
}

// PreviewResetAll materializes the row-count preview the operator
// confirms against and returns an HMAC-signed token bound to
// (admin_did, total_rows, exp, scope=reset_all). Mirrors the
// PreviewPurgeActor contract; see internal/graphql/admin/purge.go.
//
// ResetAll is strictly more destructive than PurgeActor (wipes the
// whole index, not one actor) and therefore must be at least as
// hardened: same HMAC signer, same single-use + count-drift + scope
// binding, same audit-log shape.
func (r *Resolver) PreviewResetAll(ctx context.Context) (map[string]interface{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if r.purgeTokenSigner == nil {
		return nil, fmt.Errorf("resetAll mutation is not configured")
	}
	adminDID, _ := ctx.Value(contextKeyUserDID).(string)
	if adminDID == "" {
		return nil, fmt.Errorf("admin DID missing from context")
	}

	tables, totalRows, err := r.resetAllCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("count rows: %w", err)
	}

	// TargetDID is empty for the reset_all scope — the operation
	// has no single target. The signer's admin + count + scope
	// bindings carry the security contract.
	token, exp, err := r.purgeTokenSigner.Sign(ScopeResetAll, adminDID, "", totalRows)
	if err != nil {
		return nil, fmt.Errorf("sign reset token: %w", err)
	}

	return map[string]interface{}{
		"totalRows":       totalRows,
		"tables":          tables,
		"confirmToken":    token,
		"tokenExpiresAt":  exp.UTC().Format(time.RFC3339),
		"tokenTtlSeconds": int(purgeTokenTTL / time.Second),
	}, nil
}

// resetAllCounts returns per-table row counts plus the sum across
// every entry in resetAllTables. Each count is a separate query;
// the list is small (<20) so the round-trips are noise next to the
// destructive delete that follows.
func (r *Resolver) resetAllCounts(ctx context.Context) ([]map[string]interface{}, int64, error) {
	db := r.repos.Records.DB()
	tables := make([]map[string]interface{}, 0, len(resetAllTables))
	var total int64
	for _, table := range resetAllTables {
		// Table names are hard-listed package constants; no SQL
		// injection surface. quoteIdent defends future
		// contributors who copy-paste this loop with a different
		// source.
		var count int64
		// nolint:gosec // table names are validated package constants
		query := fmt.Sprintf("SELECT COUNT(*) FROM %s", quoteIdent(table))
		if err := db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			return nil, 0, fmt.Errorf("count %s: %w", table, err)
		}
		tables = append(tables, map[string]interface{}{
			"name":  table,
			"count": count,
		})
		total += count
	}
	return tables, total, nil
}

// quoteIdent quotes a SQL identifier for Postgres. The input is
// always a hard-listed table name from this package; this is
// defense-in-depth for future code that might pass through
// user-controlled input.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// ResetAll verifies the bound HMAC token, then truncates every
// table in resetAllTables inside a single transaction. The whole
// set commits or rolls back atomically. On commit, emits the
// structured audit log line documented in SECURITY.md.
func (r *Resolver) ResetAll(ctx context.Context, confirmToken string) (map[string]interface{}, error) {
	if err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if r.purgeTokenSigner == nil {
		return nil, fmt.Errorf("resetAll mutation is not configured")
	}
	if confirmToken == "" {
		return nil, fmt.Errorf("confirmToken is required")
	}
	adminDID, _ := ctx.Value(contextKeyUserDID).(string)
	if adminDID == "" {
		return nil, fmt.Errorf("admin DID missing from context")
	}

	// Re-count under fresh state so the token-bound total is
	// verified against current rows. A drift between preview and
	// confirm rejects with ErrPurgeTokenCountDrift and the
	// operator re-previews — exactly the actor-purge flow.
	_, totalRows, err := r.resetAllCounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("count rows: %w", err)
	}
	// VerifyReason returns the bounded metric reason (one of
	// metrics.PurgeReason*) so a token-forge attempt against the
	// resetAll surface is visible in
	// hypergoat_purge_token_rejected_total just like an actor-purge
	// forge attempt. Metric increments before the early-return so
	// every failure mode is observed.
	if reason, err := r.purgeTokenSigner.VerifyReason(confirmToken, ScopeResetAll, adminDID, "", totalRows); err != nil {
		metrics.PurgeTokenRejected(reason)
		return nil, err
	}

	tx, err := r.repos.Records.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var totalDeleted int64
	for _, table := range resetAllTables {
		// nolint:gosec // table names are hard-listed package constants
		query := fmt.Sprintf("DELETE FROM %s", quoteIdent(table))
		res, err := tx.ExecContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("delete from %s: %w", table, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			totalDeleted += n
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit reset tx: %w", err)
	}

	// Structured audit log — see SECURITY.md operator contract
	// for the retention requirement (≥90d GDPR-minimum, 1y
	// recommended). Shape mirrors actor_purge so log shippers
	// can route both with one rule. requested_by_did flows through
	// logsafe.DID — defense-in-depth even though the admin DID
	// was already validated by the auth middleware.
	slog.Info("admin reset_all",
		"event", "reset_all",
		"requested_by_did", logsafe.DID(adminDID),
		"rows_deleted", totalDeleted,
		"tables_affected", len(resetAllTables),
		"ts", time.Now().UTC().Format(time.RFC3339),
	)
	metrics.ResetAllCompleted()

	return map[string]interface{}{
		"rowsDeleted":    totalDeleted,
		"tablesAffected": len(resetAllTables),
	}, nil
}

// PopulateActivity creates activity entries from existing records in the database.
// This is useful after a backfill to populate the activity dashboard with historical data.
func (r *Resolver) PopulateActivity(ctx context.Context) (int64, error) {
	// First clear existing activity to avoid duplicates
	if err := r.repos.Activity.DeleteAll(ctx); err != nil {
		return 0, fmt.Errorf("failed to clear existing activity: %w", err)
	}

	var count int64
	_, err := r.repos.Records.IterateAll(ctx, 1000, func(rec *repositories.Record) error {
		// Extract createdAt from the record JSON
		timestamp := atproto.ExtractCreatedAt(rec.JSON, time.Now())

		// Log as a successful create operation
		if _, logErr := r.repos.Activity.LogActivityWithStatus(ctx, timestamp, "create", rec.Collection, rec.DID, rec.RKey, rec.JSON, "success"); logErr == nil {
			count++
		}
		return nil
	})

	if err != nil {
		return count, fmt.Errorf("error iterating records: %w", err)
	}

	return count, nil
}

// CreateLabel creates a new label on a record or account. The admin
// creates labels under this server's domain DID, so we check the
// label definition under that same src — a label value defined
// elsewhere by a remote labeler doesn't authorise this server to
// emit it.
func (r *Resolver) CreateLabel(ctx context.Context, uri, val string, cid, exp *string) (map[string]interface{}, error) {
	// Validate URI format
	if !repositories.IsValidSubjectURI(uri) {
		return nil, fmt.Errorf("invalid subject URI: must start with 'at://' or 'did:'")
	}

	// Validate label value is defined for this server's labeler src.
	// Pre-seeded Bluesky values live under SystemLabelerSrc, so we
	// also accept those as a fallback for the built-in takedown
	// vocabulary.
	exists, err := r.repos.LabelDefinitions.Exists(ctx, r.domainDID, val)
	if err != nil {
		return nil, fmt.Errorf("failed to check label definition: %w", err)
	}
	if !exists {
		// Fallback: accept pre-seeded system labels like !takedown
		// without requiring the admin to pre-create them under the
		// domain DID.
		systemExists, err := r.repos.LabelDefinitions.Exists(ctx, repositories.SystemLabelerSrc, val)
		if err != nil {
			return nil, fmt.Errorf("failed to check label definition: %w", err)
		}
		if !systemExists {
			return nil, fmt.Errorf("label value '%s' not defined for this labeler", val)
		}
	}

	// Parse expiration if provided
	var expTime *time.Time
	if exp != nil {
		t, err := time.Parse(time.RFC3339, *exp)
		if err != nil {
			return nil, fmt.Errorf("invalid expiration format: %w", err)
		}
		expTime = &t
	}

	label, err := r.repos.Labels.Insert(ctx, r.domainDID, uri, cid, val, nil, expTime)
	if err != nil {
		return nil, fmt.Errorf("failed to create label: %w", err)
	}

	result := map[string]interface{}{
		"id":  label.ID,
		"src": label.Src,
		"uri": label.URI,
		"val": label.Val,
		"neg": label.Neg,
		"cts": label.Cts.Format(time.RFC3339),
	}
	if label.CID != nil {
		result["cid"] = *label.CID
	}
	if label.Exp != nil {
		result["exp"] = label.Exp.Format(time.RFC3339)
	}

	return result, nil
}

// NegateLabel retracts a label from a record or account.
func (r *Resolver) NegateLabel(ctx context.Context, uri, val string) (map[string]interface{}, error) {
	// Validate URI format
	if !repositories.IsValidSubjectURI(uri) {
		return nil, fmt.Errorf("invalid subject URI: must start with 'at://' or 'did:'")
	}

	label, err := r.repos.Labels.InsertNegation(ctx, r.domainDID, uri, val, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to negate label: %w", err)
	}

	return map[string]interface{}{
		"id":  label.ID,
		"src": label.Src,
		"uri": label.URI,
		"val": label.Val,
		"neg": label.Neg,
		"cts": label.Cts.Format(time.RFC3339),
	}, nil
}

// CreateLabelDefinition creates a new label definition under this
// server's labeler (r.domainDID). Admins can still seed globally-
// scoped system labels by passing src = SystemLabelerSrc explicitly
// via the admin GraphQL mutation — see admin/schema.go.
func (r *Resolver) CreateLabelDefinition(ctx context.Context, src, val, description, severity string, defaultVisibility *string) (map[string]interface{}, error) {
	if src == "" {
		src = r.domainDID
	}

	// Bound the stringy fields so the admin API can't blow up the DB
	// with multi-megabyte values. The wire-side labeler ingest path
	// already caps these via labeler.MaxLabelValLen et al; mirror
	// those limits here.
	if val == "" {
		return nil, fmt.Errorf("val is required")
	}
	if len(val) > 128 {
		return nil, fmt.Errorf("val must be at most 128 bytes")
	}
	if len(description) > 1024 {
		return nil, fmt.Errorf("description must be at most 1024 bytes")
	}
	if len(src) > 512 {
		return nil, fmt.Errorf("src must be at most 512 bytes")
	}

	// Validate severity
	sev, err := repositories.ValidateSeverity(severity)
	if err != nil {
		return nil, err
	}

	// Default visibility
	vis := repositories.VisibilityWarn
	if defaultVisibility != nil {
		vis, err = repositories.ValidateVisibility(*defaultVisibility)
		if err != nil {
			return nil, err
		}
	}

	// Check if already exists for this labeler
	exists, err := r.repos.LabelDefinitions.Exists(ctx, src, val)
	if err != nil {
		return nil, fmt.Errorf("failed to check label definition: %w", err)
	}
	if exists {
		return nil, fmt.Errorf("label '%s' already exists for labeler %s", val, src)
	}

	if err := r.repos.LabelDefinitions.Insert(ctx, src, val, description, sev, vis); err != nil {
		return nil, fmt.Errorf("failed to create label definition: %w", err)
	}

	def, err := r.repos.LabelDefinitions.Get(ctx, src, val)
	if err != nil {
		return nil, fmt.Errorf("failed to retrieve created definition: %w", err)
	}

	return map[string]interface{}{
		"src":               def.Src,
		"val":               def.Val,
		"description":       def.Description,
		"severity":          string(def.Severity),
		"defaultVisibility": string(def.DefaultVisibility),
		"createdAt":         def.CreatedAt.Format(time.RFC3339),
	}, nil
}

// ResolveReport resolves a moderation report.
func (r *Resolver) ResolveReport(ctx context.Context, id int64, action string, labelVal *string, resolverDID string) (map[string]interface{}, error) {
	// Get the report
	report, err := r.repos.Reports.Get(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("report not found")
		}
		return nil, fmt.Errorf("failed to get report: %w", err)
	}

	var status repositories.ReportStatus
	switch action {
	case "apply_label":
		if labelVal == nil {
			return nil, fmt.Errorf("labelVal required for apply_label action")
		}
		// Apply the label
		_, err := r.repos.Labels.Insert(ctx, r.domainDID, report.SubjectURI, nil, *labelVal, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to apply label: %w", err)
		}
		status = repositories.StatusResolved
	case "dismiss":
		status = repositories.StatusDismissed
	default:
		return nil, fmt.Errorf("invalid action: %s", action)
	}

	// Update report status
	updatedReport, err := r.repos.Reports.Resolve(ctx, id, status, resolverDID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve report: %w", err)
	}

	result := map[string]interface{}{
		"id":          updatedReport.ID,
		"reporterDid": updatedReport.ReporterDID,
		"subjectUri":  updatedReport.SubjectURI,
		"reasonType":  string(updatedReport.ReasonType),
		"status":      string(updatedReport.Status),
		"createdAt":   updatedReport.CreatedAt.Format(time.RFC3339),
	}
	if updatedReport.Reason != nil {
		result["reason"] = *updatedReport.Reason
	}
	if updatedReport.ResolvedBy != nil {
		result["resolvedBy"] = *updatedReport.ResolvedBy
	}
	if updatedReport.ResolvedAt != nil {
		result["resolvedAt"] = updatedReport.ResolvedAt.Format(time.RFC3339)
	}

	return result, nil
}
