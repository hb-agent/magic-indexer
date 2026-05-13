package migrations_test

import (
	"embed"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/GainForest/hypergoat/internal/database/migrations"
)

// indexNameRegex captures the index name from
//
//	CREATE [UNIQUE] INDEX [CONCURRENTLY] [IF NOT EXISTS] <name> ON …
//
// Migration files use various combinations of these clauses (013 uses
// CONCURRENTLY + IF NOT EXISTS; 001 uses just IF NOT EXISTS). The regex
// matches all of them. Multi-line tolerant via the leading `(?m)`.
var indexNameRegex = regexp.MustCompile(`(?im)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\s+ON\b`)

// dropIndexNameRegex matches DROP INDEX [CONCURRENTLY] [IF EXISTS] <name>.
// Used to "clear" historic duplicates: a CREATE that's followed by a DROP
// in a later migration before another CREATE-with-the-same-name is not a
// collision because the second CREATE is replacing, not duplicating.
var dropIndexNameRegex = regexp.MustCompile(`(?im)^\s*DROP\s+INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)\b`)

// TestMigrations_UniqueIndexNames parses every up migration and asserts
// no two files declare a CREATE INDEX with the same index name.
//
// This existed because migration 013 reused 001's idx_record_json_gin name
// (with IF NOT EXISTS), making 013 a silent no-op against any environment
// that ran 001 first — and 013's down dropped the 001 index, permanently
// degrading rolled-back environments. The post-21 fix renamed 013's target
// to idx_record_json_gin_path_ops; this test guards against a recurrence.
//
// Runs at the package level (no Postgres needed) so CI always exercises it.
func TestMigrations_UniqueIndexNames(t *testing.T) {
	fs := migrations.PostgresFS()

	entries, err := fs.ReadDir("postgres")
	if err != nil {
		t.Fatalf("read migration dir: %v", err)
	}

	// We walk migrations in lexicographic-filename order (which the
	// migration runner does too). For each index name, track whether
	// it is currently "live" (created since the last DROP). A second
	// CREATE while the name is live is a duplicate.
	type site struct {
		file string
		line int
	}
	live := make(map[string]site)    // index name → original CREATE site
	dupes := make(map[string][]site) // index name → list of duplicate CREATE sites

	// Sort filenames so the walk order is deterministic and matches
	// the runner. Migrations are named 001_*, 013_*, etc. — string
	// sort is correct because the leading digits are zero-padded.
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}
		files = append(files, name)
	}
	sortStrings(files)

	for _, name := range files {
		body, err := fs.ReadFile(path.Join("postgres", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, line := range splitLinesWithNumbers(string(body)) {
			if m := indexNameRegex.FindStringSubmatch(line.text); m != nil {
				indexName := strings.ToLower(m[1])
				if _, alreadyLive := live[indexName]; alreadyLive {
					dupes[indexName] = append(dupes[indexName], site{file: name, line: line.num})
				} else {
					live[indexName] = site{file: name, line: line.num}
				}
				continue
			}
			if m := dropIndexNameRegex.FindStringSubmatch(line.text); m != nil {
				indexName := strings.ToLower(m[1])
				delete(live, indexName)
			}
		}
	}

	if len(dupes) > 0 {
		var lines []string
		for idx, sites := range dupes {
			origin := live[idx]
			parts := []string{idx + " first created at " + origin.file + ":" + itoa(origin.line) + ", duplicated at"}
			for _, s := range sites {
				parts = append(parts, "    "+s.file+":"+itoa(s.line))
			}
			lines = append(lines, strings.Join(parts, "\n"))
		}
		t.Errorf("duplicate CREATE INDEX names across migrations (each name must be unique unless a preceding DROP INDEX retires it — IF NOT EXISTS otherwise silently no-ops the follow-up migration):\n  %s",
			strings.Join(lines, "\n  "))
	}
}

func sortStrings(s []string) {
	// Insertion sort; the migration set is small.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// PostgresFS is exposed by the migrations package so this guard-test can
// read the embedded files. If the helper is renamed there, this test
// will fail to compile — which is the kind of guard-rail break that
// reads as "be careful here."
var _ embed.FS = migrations.PostgresFS()

type numberedLine struct {
	num  int
	text string
}

func splitLinesWithNumbers(s string) []numberedLine {
	lines := strings.Split(s, "\n")
	out := make([]numberedLine, len(lines))
	for i, l := range lines {
		out[i] = numberedLine{num: i + 1, text: l}
	}
	return out
}

// itoa keeps the regex / strings imports tidy and avoids depending on
// strconv just for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
