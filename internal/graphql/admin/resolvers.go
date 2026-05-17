package admin

// =============================================================================
// Admin resolver — core surface
//
// resolvers.go was 1729 lines as of 2026-05-17. Track 5 of
// review-2026-05-17 split it along feature seams. This file keeps:
//
//   - Resolver struct, NewResolver, Repositories, callback types
//   - Set*Callback wiring (BackfillCallback, FullBackfillCallback,
//     LexiconChangeCallback, SchemaValidateCallback,
//     ProcessRestartCallback)
//   - Statistics, CurrentSession, Settings, UpdateSettings
//   - AddAdmin, RemoveAdmin — they mutate admin_dids, which is part
//     of the same settings surface as UpdateSettings(adminDids=…),
//     and share the auditSettingsChanged audit-log shape
//   - IsBackfilling, SetBackfillActive
//   - validateOperatorURL, validateJetstreamURL, auditSettingsChanged,
//     maxAdminPageSize, clampAdminPageSize
//
// Where the rest lives (alphabetical):
//
//   resolvers_activity.go      ActivityBuckets, CollectionOverview,
//                              RecentActivity, ValidationStats,
//                              PopulateActivity
//   resolvers_backfill.go      TriggerBackfill, BackfillActor
//   resolvers_labels.go        LabelDefinitions, ViewerLabelPreferences,
//                              Labels, Reports, CreateLabel, NegateLabel,
//                              CreateLabelDefinition, ResolveReport
//   resolvers_lexicons.go      Lexicons, UploadLexicons,
//                              RegisterLexicon, DeleteLexicon,
//                              CreateFieldIndex, DropFieldIndex,
//                              notifyLexiconChange
//   resolvers_oauth_clients.go OAuthClients, CreateOAuthClient,
//                              UpdateOAuthClient, DeleteOAuthClient
//   purge.go                   PreviewPurgeActor, PurgeActor,
//                              PreviewResetAll, ResetAll,
//                              resetAllCounts, resetAllTables,
//                              quoteIdent
//
// When adding a new admin method: pick the feature file that owns
// the surface; reach for purge.go for any destructive op; only put
// new code in this file when it's part of the core wiring above
// (Settings, callbacks, or otherwise touching the Resolver struct's
// direct state).
// =============================================================================

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	didpkg "github.com/GainForest/hypergoat/internal/atproto/did"
	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/logsafe"
	"github.com/GainForest/hypergoat/internal/metrics"
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
