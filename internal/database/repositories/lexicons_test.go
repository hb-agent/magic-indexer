package repositories_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/testutil"
)

func setupLexiconsTest(t *testing.T) *repositories.LexiconsRepository {
	t.Helper()
	db := testutil.SetupTestDB(t)
	return db.Lexicons
}

// jsonEqual compares two JSON strings semantically (key order independent).
func jsonEqual(a, b string) bool {
	var ja, jb interface{}
	if err := json.Unmarshal([]byte(a), &ja); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(b), &jb); err != nil {
		return false
	}
	na, _ := json.Marshal(ja)
	nb, _ := json.Marshal(jb)
	return string(na) == string(nb)
}

func TestLexiconsRepository_Upsert(t *testing.T) {
	repo := setupLexiconsTest(t)
	ctx := context.Background()

	// Insert new lexicon
	jsonData := `{"lexicon":1,"id":"app.bsky.feed.post"}`
	err := repo.Upsert(ctx, "app.bsky.feed.post", jsonData)
	if err != nil {
		t.Fatalf("failed to insert lexicon: %v", err)
	}

	lex, err := repo.GetByID(ctx, "app.bsky.feed.post")
	if err != nil {
		t.Fatalf("failed to get lexicon after insert: %v", err)
	}
	if lex.ID != "app.bsky.feed.post" {
		t.Errorf("ID = %q, want %q", lex.ID, "app.bsky.feed.post")
	}
	if !jsonEqual(lex.JSON, jsonData) {
		t.Errorf("JSON = %q, want %q", lex.JSON, jsonData)
	}

	// Update existing lexicon with new JSON
	updatedJSON := `{"lexicon":1,"id":"app.bsky.feed.post","revision":2}`
	err = repo.Upsert(ctx, "app.bsky.feed.post", updatedJSON)
	if err != nil {
		t.Fatalf("failed to upsert lexicon: %v", err)
	}

	lex, err = repo.GetByID(ctx, "app.bsky.feed.post")
	if err != nil {
		t.Fatalf("failed to get lexicon after upsert: %v", err)
	}
	if !jsonEqual(lex.JSON, updatedJSON) {
		t.Errorf("JSON after upsert = %q, want %q", lex.JSON, updatedJSON)
	}
}

func TestLexiconsRepository_GetByID(t *testing.T) {
	repo := setupLexiconsTest(t)
	ctx := context.Background()

	// Setup: insert a lexicon
	err := repo.Upsert(ctx, "app.bsky.feed.post", `{"lexicon":1,"id":"app.bsky.feed.post"}`)
	if err != nil {
		t.Fatalf("failed to insert lexicon: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		lex, err := repo.GetByID(ctx, "app.bsky.feed.post")
		if err != nil {
			t.Fatalf("GetByID() error = %v", err)
		}
		if lex.ID != "app.bsky.feed.post" {
			t.Errorf("ID = %q, want %q", lex.ID, "app.bsky.feed.post")
		}
		if !jsonEqual(lex.JSON, `{"lexicon":1,"id":"app.bsky.feed.post"}`) {
			t.Errorf("JSON = %q, want %q", lex.JSON, `{"lexicon":1,"id":"app.bsky.feed.post"}`)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.GetByID(ctx, "app.bsky.nonexistent")
		if err == nil {
			t.Fatal("GetByID() expected error for non-existing ID, got nil")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("GetByID() error = %v, want sql.ErrNoRows", err)
		}
	})
}

func TestLexiconsRepository_GetAll(t *testing.T) {
	repo := setupLexiconsTest(t)
	ctx := context.Background()

	t.Run("empty", func(t *testing.T) {
		lexicons, err := repo.GetAll(ctx)
		if err != nil {
			t.Fatalf("GetAll() error = %v", err)
		}
		if len(lexicons) != 0 {
			t.Errorf("GetAll() on empty db returned %d items, want 0", len(lexicons))
		}
	})

	t.Run("after inserts", func(t *testing.T) {
		// Insert in non-alphabetical order to verify ORDER BY id
		err := repo.Upsert(ctx, "app.bsky.feed.post", `{"lexicon":1,"id":"app.bsky.feed.post"}`)
		if err != nil {
			t.Fatalf("failed to insert lexicon: %v", err)
		}
		err = repo.Upsert(ctx, "app.bsky.actor.profile", `{"lexicon":1,"id":"app.bsky.actor.profile"}`)
		if err != nil {
			t.Fatalf("failed to insert lexicon: %v", err)
		}

		lexicons, err := repo.GetAll(ctx)
		if err != nil {
			t.Fatalf("GetAll() error = %v", err)
		}
		if len(lexicons) != 2 {
			t.Fatalf("GetAll() returned %d, want 2", len(lexicons))
		}
		// Should be sorted by ID
		if lexicons[0].ID != "app.bsky.actor.profile" {
			t.Errorf("first lexicon = %q, want app.bsky.actor.profile", lexicons[0].ID)
		}
		if lexicons[1].ID != "app.bsky.feed.post" {
			t.Errorf("second lexicon = %q, want app.bsky.feed.post", lexicons[1].ID)
		}
	})
}

func TestLexiconsRepository_Delete(t *testing.T) {
	repo := setupLexiconsTest(t)
	ctx := context.Background()

	// Insert then delete
	err := repo.Upsert(ctx, "app.bsky.feed.post", `{"lexicon":1,"id":"app.bsky.feed.post"}`)
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	err = repo.Delete(ctx, "app.bsky.feed.post")
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	_, err = repo.GetByID(ctx, "app.bsky.feed.post")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("GetByID after delete: error = %v, want sql.ErrNoRows", err)
	}
}
