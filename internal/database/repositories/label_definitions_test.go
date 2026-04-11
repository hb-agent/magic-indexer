package repositories_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/testutil"
)

const testLabelerSrc = "did:plc:testlabeler"

func setupLabelDefsTest(t *testing.T) *repositories.LabelDefinitionsRepository {
	t.Helper()
	db := testutil.SetupTestDB(t)
	return db.LabelDefinitions
}

func TestLabelDefinitions_Insert(t *testing.T) {
	repo := setupLabelDefsTest(t)
	ctx := context.Background()

	err := repo.Insert(ctx, testLabelerSrc, "test-custom-label", "Custom test label", repositories.SeverityInform, repositories.VisibilityWarn)
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	// Verify it was inserted
	def, err := repo.Get(ctx, testLabelerSrc, "test-custom-label")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if def.Src != testLabelerSrc {
		t.Errorf("Src = %q, want %q", def.Src, testLabelerSrc)
	}
	if def.Val != "test-custom-label" {
		t.Errorf("Val = %q, want %q", def.Val, "test-custom-label")
	}
	if def.Description != "Custom test label" {
		t.Errorf("Description = %q, want %q", def.Description, "Custom test label")
	}
	if def.Severity != repositories.SeverityInform {
		t.Errorf("Severity = %q, want %q", def.Severity, repositories.SeverityInform)
	}
	if def.DefaultVisibility != repositories.VisibilityWarn {
		t.Errorf("DefaultVisibility = %q, want %q", def.DefaultVisibility, repositories.VisibilityWarn)
	}
}

func TestLabelDefinitions_PerLabelerSemantics(t *testing.T) {
	repo := setupLabelDefsTest(t)
	ctx := context.Background()

	// Two distinct labelers define the same val with different severities.
	if err := repo.Insert(ctx, "did:plc:labelerA", "spicy", "A says spicy content", repositories.SeverityInform, repositories.VisibilityWarn); err != nil {
		t.Fatalf("insert labelerA: %v", err)
	}
	if err := repo.Insert(ctx, "did:plc:labelerB", "spicy", "B says hide", repositories.SeverityAlert, repositories.VisibilityHide); err != nil {
		t.Fatalf("insert labelerB: %v", err)
	}

	defA, err := repo.Get(ctx, "did:plc:labelerA", "spicy")
	if err != nil {
		t.Fatalf("get labelerA: %v", err)
	}
	defB, err := repo.Get(ctx, "did:plc:labelerB", "spicy")
	if err != nil {
		t.Fatalf("get labelerB: %v", err)
	}

	if defA.Severity != repositories.SeverityInform {
		t.Errorf("labelerA Severity = %q, want inform", defA.Severity)
	}
	if defB.Severity != repositories.SeverityAlert {
		t.Errorf("labelerB Severity = %q, want alert", defB.Severity)
	}
	if defA.DefaultVisibility == defB.DefaultVisibility {
		t.Errorf("expected distinct visibilities; both were %q", defA.DefaultVisibility)
	}
}

func TestLabelDefinitions_Get(t *testing.T) {
	repo := setupLabelDefsTest(t)
	ctx := context.Background()

	err := repo.Insert(ctx, testLabelerSrc, "test-get-label", "Label for get test", repositories.SeverityInform, repositories.VisibilityWarn)
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	t.Run("found", func(t *testing.T) {
		def, err := repo.Get(ctx, testLabelerSrc, "test-get-label")
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if def.Val != "test-get-label" {
			t.Errorf("Val = %q, want %q", def.Val, "test-get-label")
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := repo.Get(ctx, testLabelerSrc, "nonexistent")
		if err == nil {
			t.Fatal("Get() expected error for non-existing val, got nil")
		}
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("Get() error = %v, want sql.ErrNoRows", err)
		}
	})

	t.Run("wrong src is not found", func(t *testing.T) {
		_, err := repo.Get(ctx, "did:plc:other", "test-get-label")
		if err == nil {
			t.Fatal("Get() expected error for wrong src, got nil")
		}
	})
}

func TestLabelDefinitions_GetAll(t *testing.T) {
	repo := setupLabelDefsTest(t)
	ctx := context.Background()

	// Insert a custom definition in addition to seeded ones.
	err := repo.Insert(ctx, testLabelerSrc, "zzz-test-custom", "Custom for GetAll", repositories.SeverityInform, repositories.VisibilityWarn)
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	defs, err := repo.GetAll(ctx)
	if err != nil {
		t.Fatalf("GetAll() error = %v", err)
	}

	// Migrations seed 11 definitions under SystemLabelerSrc; we added 1 more.
	if len(defs) < 12 {
		t.Errorf("GetAll() returned %d definitions, want at least 12", len(defs))
	}

	// The custom (src, val) pair should appear.
	found := false
	for _, def := range defs {
		if def.Src == testLabelerSrc && def.Val == "zzz-test-custom" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("GetAll() did not return the custom (%s, zzz-test-custom) row", testLabelerSrc)
	}
}

func TestLabelDefinitions_GetNonSystem(t *testing.T) {
	repo := setupLabelDefsTest(t)
	ctx := context.Background()

	// Insert a custom non-system label under a real labeler, and a
	// system-prefixed label under that same labeler (it still gets
	// filtered because the val starts with '!').
	err := repo.Insert(ctx, testLabelerSrc, "test-nonsys", "Non-system label", repositories.SeverityInform, repositories.VisibilityWarn)
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}
	err = repo.Insert(ctx, testLabelerSrc, "!test-sys", "System test label", repositories.SeverityTakedown, repositories.VisibilityHide)
	if err != nil {
		t.Fatalf("Insert() error = %v", err)
	}

	defs, err := repo.GetNonSystem(ctx)
	if err != nil {
		t.Fatalf("GetNonSystem() error = %v", err)
	}

	// Verify no rows from the system src are returned, and no !-prefixed vals.
	for _, def := range defs {
		if def.Src == repositories.SystemLabelerSrc {
			t.Errorf("GetNonSystem() included system-src row: %+v", def)
		}
		if len(def.Val) > 0 && def.Val[0] == '!' {
			t.Errorf("GetNonSystem() included system-prefix val %q", def.Val)
		}
	}

	// Verify our custom non-system label is present.
	found := false
	for _, def := range defs {
		if def.Src == testLabelerSrc && def.Val == "test-nonsys" {
			found = true
			break
		}
	}
	if !found {
		t.Error("GetNonSystem() did not include (test-labeler, test-nonsys)")
	}
}

func TestLabelDefinitions_Exists(t *testing.T) {
	repo := setupLabelDefsTest(t)
	ctx := context.Background()

	// Use a seeded system label for the "existing" check.
	t.Run("existing system label", func(t *testing.T) {
		exists, err := repo.Exists(ctx, repositories.SystemLabelerSrc, "spam")
		if err != nil {
			t.Fatalf("Exists() error = %v", err)
		}
		if !exists {
			t.Error("Exists() = false, want true for seeded system label")
		}
	})

	t.Run("existing system label under wrong src is missing", func(t *testing.T) {
		// The seeded label is under SystemLabelerSrc; querying it
		// under a different src should not find it.
		exists, err := repo.Exists(ctx, "did:plc:other", "spam")
		if err != nil {
			t.Fatalf("Exists() error = %v", err)
		}
		if exists {
			t.Error("Exists() = true, want false for seeded label under wrong src")
		}
	})

	t.Run("non-existing", func(t *testing.T) {
		exists, err := repo.Exists(ctx, testLabelerSrc, "nonexistent")
		if err != nil {
			t.Fatalf("Exists() error = %v", err)
		}
		if exists {
			t.Error("Exists() = true, want false for non-existing label")
		}
	})
}

func TestValidateVisibility(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    repositories.LabelVisibility
		wantErr bool
	}{
		{name: "ignore", input: "ignore", want: repositories.VisibilityIgnore},
		{name: "show", input: "show", want: repositories.VisibilityShow},
		{name: "warn", input: "warn", want: repositories.VisibilityWarn},
		{name: "hide", input: "hide", want: repositories.VisibilityHide},
		{name: "invalid", input: "invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repositories.ValidateVisibility(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateVisibility(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ValidateVisibility(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestValidateSeverity(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    repositories.LabelSeverity
		wantErr bool
	}{
		{name: "inform", input: "inform", want: repositories.SeverityInform},
		{name: "alert", input: "alert", want: repositories.SeverityAlert},
		{name: "takedown", input: "takedown", want: repositories.SeverityTakedown},
		{name: "invalid", input: "invalid", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repositories.ValidateSeverity(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateSeverity(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("ValidateSeverity(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
