package main

// Raw HTTP admin endpoints. Lives alongside main.go in package main
// so it has direct access to *services and *backgroundServices
// without exporting them. Extracted from setupRouter() in the
// 2026-05-17 Track 6 refactor; see docs/review-2026-05-17/plan.md.

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	didpkg "github.com/GainForest/hypergoat/internal/atproto/did"
	"github.com/GainForest/hypergoat/internal/labeler"
)

// checkAdminBearer validates the ADMIN_API_KEY bearer token on a
// raw HTTP admin endpoint. Returns true if the caller should
// proceed; otherwise writes the error response and returns false.
// Centralised so every /admin/* raw HTTP route uses the same
// constant-time comparison path. adminAPIKey of "" disables the
// route entirely (returns 403) — the operator must set
// ADMIN_API_KEY to enable these endpoints.
func checkAdminBearer(adminAPIKey string, w http.ResponseWriter, req *http.Request) bool {
	if adminAPIKey == "" {
		http.Error(w, "admin endpoint disabled: ADMIN_API_KEY is not configured", http.StatusForbidden)
		return false
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return false
	}
	token := strings.TrimPrefix(auth, "Bearer ")
	if subtle.ConstantTimeCompare([]byte(token), []byte(adminAPIKey)) != 1 {
		http.Error(w, "invalid bearer token", http.StatusUnauthorized)
		return false
	}
	return true
}

// newLabelerResetHandler returns the POST /admin/labeler/reset
// handler — operator escape hatch to force a re-backfill on next
// startup for a specific labeler DID. Deletes both the
// subscription seq cursor and any in-progress backfill checkpoint.
func newLabelerResetHandler(adminAPIKey string, svc *services) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !checkAdminBearer(adminAPIKey, w, req) {
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
		if !didpkg.IsValid(did) {
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
	}
}

// newLabelerPauseHandler returns the POST /admin/labeler/pause
// handler — pauses a single labeler subscription without
// restarting the process. Calls Stop() on the matching consumer
// and removes it from the labelerConsumers slice. The consumer's
// cursor is flushed before Stop returns, so resume on next
// startup picks up where it left off. A restart is still required
// to bring the consumer back up (we do not currently support
// in-process resume); this endpoint is for incident response.
func newLabelerPauseHandler(adminAPIKey string, bg *backgroundServices) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !checkAdminBearer(adminAPIKey, w, req) {
			return
		}
		did := req.URL.Query().Get("did")
		if did == "" {
			http.Error(w, "missing did query parameter", http.StatusBadRequest)
			return
		}
		if !didpkg.IsValid(did) {
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
	}
}

// newLabelChainHandler returns the GET /admin/label-chain handler —
// returns every label (active, negated, expired) on a single URI
// with src + val + neg + cts + exp so an operator can answer "why
// is this record hidden?" without attaching a debugger. Deliberately
// bypasses the exp / neg filters of the public query path — this
// is a diagnostic view.
func newLabelChainHandler(adminAPIKey string, svc *services) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if !checkAdminBearer(adminAPIKey, w, req) {
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
	}
}
