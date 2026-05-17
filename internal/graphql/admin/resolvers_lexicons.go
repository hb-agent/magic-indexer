package admin

// Admin resolvers for lexicon CRUD and field-index management.
// Includes the post-upload notifyLexiconChange helper, the
// per-upload size caps, and the field-index admin endpoints.
// Extracted from resolvers.go in 2026-05-17 Track 5; see
// docs/review-2026-05-17/plan.md for the partition rationale.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/GainForest/hypergoat/internal/lexicon"
)

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
