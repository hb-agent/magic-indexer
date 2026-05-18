package schema

import (
	"fmt"
	"log/slog"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/graphql/resolver"
	"github.com/GainForest/hypergoat/internal/graphql/types"
)

// derivedFieldDescriptor describes a synthetic field that lives on
// a per-collection record GraphQL type but is computed by a Resolve
// function rather than read from the record's JSON. v1 entries:
// awardCount (issue #89) on app.certified.badge.definition.
//
// SECURITY: derived-field resolvers run with the request context's
// repositories handle. The Resolve func is registry-defined; all
// SQL stays inside the repositories package and uses parameter
// binding, never request-data string concatenation. Same contract
// as joinedWhereDescriptor / arrayWhereDescriptor.
type derivedFieldDescriptor struct {
	FieldName string
	Field     *graphql.Field // Type + Description + Resolve in one
}

// awardCountDescription is pinned verbatim so consumers see the
// policy at schema introspection. Drift is pinned by
// TestDerivedFieldRegistry_BadgeDefinitionAwardCount.
const awardCountDescription = `Number of app.certified.badge.award records whose badge strongRef points at this definition. Independent of the award subject (returns the total count of awards strong-ref'ing this definition across all subjects + DIDs). For the certified-app's Lists section: this collapses the master-view aggregate (definitions + per-list count) to a single indexer query. The count uses an indexed lookup against a partial expression index on (json->'badge'->>'uri') filtered by collection; per-row cost scales with the matching set size — a definition with thousands of awards pays an index-range scan, not a constant-time probe. Filtered counts (e.g. by issuer or by award properties) are not yet exposed.`

// derivedFieldRegistry maps lexiconID → fieldName → descriptor for
// every synthetic record-level field that needs a custom Resolve.
//
// First entry (issue #89): app.certified.badge.definition.awardCount,
// returning the count of awards strong-ref'ing the definition.
var derivedFieldRegistry = map[string]map[string]derivedFieldDescriptor{
	"app.certified.badge.definition": {
		"awardCount": {
			FieldName: "awardCount",
			Field: &graphql.Field{
				Type:        graphql.NewNonNull(graphql.Int),
				Description: awardCountDescription,
				Resolve:     resolveAwardCount,
			},
		},
	},
}

// resolveAwardCount is the Resolve func for the awardCount derived
// field on AppCertifiedBadgeDefinitionRecord. Pulls the URI from
// the parent node map and delegates to the repository's COUNT
// helper. Per-row N+1; bounded by GraphQL page size + indexed by
// migration 030.
//
// Empty / missing URI returns (0, nil) — the resolver is NOT the
// first line of defense against malformed records (the connection
// resolver always sets sanitized["uri"] = rec.URI per
// builder.go:952). Missing repositories handle returns (0, nil)
// with a warn log, mirroring the labels resolver pattern.
func resolveAwardCount(p graphql.ResolveParams) (interface{}, error) {
	src, ok := p.Source.(map[string]interface{})
	if !ok {
		// IR1.2 (#89): the connection resolver always sets a
		// map source (builder.go:952), so this path is
		// "shouldn't happen." If a future refactor routes a
		// typed struct through here, awardCount would silently
		// return 0 without this warn — make the dead-letter
		// case observable.
		slog.WarnContext(p.Context, "awardCount: source is not a map", "type", fmt.Sprintf("%T", p.Source))
		return 0, nil
	}
	uri, _ := src["uri"].(string)
	if uri == "" {
		return 0, nil
	}
	repos := resolver.GetRepositories(p.Context)
	if repos == nil {
		slog.WarnContext(p.Context, "awardCount: repositories unavailable in context")
		return 0, nil
	}
	return repos.Records.CountAwardsByBadgeURI(p.Context, uri)
}

// derivedFieldsForObjectBuilder flattens derivedFieldRegistry into
// the map shape the types.ObjectBuilder consumes
// (lexiconID → fieldName → *graphql.Field). Called once by the
// schema Builder at construction time; the result is passed to
// NewObjectBuilderWithDerivedFields.
func derivedFieldsForObjectBuilder() map[string]map[string]*graphql.Field {
	out := make(map[string]map[string]*graphql.Field, len(derivedFieldRegistry))
	for lexID, byField := range derivedFieldRegistry {
		fields := make(map[string]*graphql.Field, len(byField))
		for fieldName, desc := range byField {
			fields[fieldName] = desc.Field
		}
		out[lexID] = fields
	}
	return out
}

// mustNotReserveField asserts at init() time that no derived field
// name collides with the reserved record metadata fields (uri,
// cid, did, rkey, labels, pds). Startup-fail mode — a colliding
// registry edit panics at `go test` / first run, before any
// schema serves traffic.
func mustNotReserveField(lexiconID, fieldName string) {
	if types.ReservedRecordFields[fieldName] {
		panic(fmt.Sprintf("derived field %q on %q collides with reserved record field — rename in derivedFieldRegistry", fieldName, lexiconID))
	}
}

func init() {
	for lexID, fields := range derivedFieldRegistry {
		for fieldName := range fields {
			mustNotReserveField(lexID, fieldName)
		}
	}
}
