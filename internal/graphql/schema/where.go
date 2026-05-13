package schema

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/atproto/did"
	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/graphql/types"
	"github.com/GainForest/hypergoat/internal/lexicon"
)

// filterDescriptor describes a single lexicon-specific filter field
// that needs a bespoke SQL shape (i.e. a non-KindScalar FieldFilter).
// One entry per (lexicon, GraphQL field name) pair lives in
// filterRegistry below; the schema builder injects the field at type
// construction, and the where-input extractor uses it to wire input
// into the right repositories.FilterKind. Field descriptions are
// pinned at the registry so consumers see the policy text at schema
// introspection.
type filterDescriptor struct {
	// Kind selects the SQL-emission strategy in
	// repositories.buildSingleFilter.
	Kind repositories.FilterKind
	// FieldName is the GraphQL input-field name (e.g. "contributor",
	// "subject"). It is also stored on the emitted FieldFilter for
	// debugging — the SQL path is hardcoded by Kind, not FieldName.
	FieldName string
	// Description is the policy text shown to consumers via schema
	// introspection. Pinned here so the GraphQL and SQL layers can
	// never drift.
	Description string
}

// contributorFieldDescription is the GraphQL field description for
// the contributor filter — pinned verbatim so consumers see the
// policy at the schema-introspection boundary. See
// docs/issue-64/plan.md "Field description (pinned text)" for
// rationale.
const contributorFieldDescription = `Filter to activities where any contributors[*].contributorIdentity resolves to one of these DIDs. DIDs only — handle values are rejected at the GraphQL layer. Records whose contributor identity is a handle (not a DID) silently do not match — handle storage is a producer-side concern, not indexed as a queryable identity here. The strong-ref contributor variant (com.atproto.repo.strongRef) is not currently supported. To express "authored OR contributed" as a single query, compose with the did filter via _or: where: { _or: [ { did: { eq: "did:plc:me" } }, { contributor: { in: ["did:plc:me"] } } ] }.`

// badgeAwardSubjectDescription is pinned verbatim so consumers see
// the policy at schema introspection.
const badgeAwardSubjectDescription = `Filter badge awards by the subject DID. Matches awards whose subject resolves to the given DID across both lexicon refs of the subject union: app.certified.defs#did (object form {did: "did:plc:..."}) and com.atproto.repo.strongRef (object form {uri: "at://did:plc:.../...", cid: "..."} — DID is the at-uri authority). DIDs only — handle values are rejected at the GraphQL layer. Compose with the did filter via _or to express "issued by me OR targeting me": where: { _or: [ { did: { eq: "did:plc:me" } }, { subject: { eq: "did:plc:me" } } ] }.`

// filterRegistry maps lexicon ID → (GraphQL input field name →
// descriptor) for every lexicon-specific filter that needs a bespoke
// SQL shape. Adding a new entry is the only place to touch when a
// new lexicon adopts an existing FilterKind; adding a new
// FilterKind is the only place to touch in repositories/filter.go
// plus a new arm in buildSingleFilter.
//
// The registry intentionally lives in the GraphQL layer — the SQL
// emitter is shape-agnostic and only sees a FilterKind, never a
// lexicon ID. Keeping the policy here means lexicon-specific UX
// (field name, description, what operators are accepted) does not
// leak into the repository package.
var filterRegistry = map[string]map[string]filterDescriptor{
	"org.hypercerts.claim.activity": {
		"contributor": {
			Kind:        repositories.KindArrayContributor,
			FieldName:   "contributor",
			Description: contributorFieldDescription,
		},
	},
	"app.certified.badge.award": {
		"subject": {
			Kind:        repositories.KindUnionSubject,
			FieldName:   "subject",
			Description: badgeAwardSubjectDescription,
		},
	},
}

// lookupFilterDescriptor returns the descriptor for a (lexicon, field)
// pair if the registry has one. The second return value reports
// presence in the standard Go idiom.
func lookupFilterDescriptor(lexID, fieldName string) (filterDescriptor, bool) {
	if byField, ok := filterRegistry[lexID]; ok {
		d, ok := byField[fieldName]
		return d, ok
	}
	return filterDescriptor{}, false
}

// buildWhereInputType generates a per-collection WhereInput InputObject type
// from the lexicon's main record definition. Returns nil if the lexicon has
// no filterable scalar properties.
func buildWhereInputType(lex *lexicon.Lexicon) *graphql.InputObject {
	if lex.Defs.Main == nil {
		return nil
	}

	fields := graphql.InputObjectConfigFieldMap{}
	recordDef := lex.Defs.Main

	for _, entry := range recordDef.Properties {
		filterType := propertyToFilterInput(entry.Property)
		if filterType == nil {
			continue
		}
		fields[entry.Name] = &graphql.InputObjectFieldConfig{
			Type:        filterType,
			Description: fmt.Sprintf("Filter by %s", entry.Name),
		}
	}

	// Always add `did` field for author filtering.
	fields["did"] = &graphql.InputObjectFieldConfig{
		Type:        types.DIDFilterInput,
		Description: "Filter by record author DID (column-level, optimized)",
	}

	// Inject any lexicon-specific filter fields from the registry.
	// Each is a DIDFilterInput today; if a future descriptor needs a
	// different input shape, extend filterDescriptor with a Type field
	// (or a factory func) and switch here.
	for _, descriptor := range filterRegistry[lex.ID] {
		fields[descriptor.FieldName] = &graphql.InputObjectFieldConfig{
			Type:        types.DIDFilterInput,
			Description: descriptor.Description,
		}
	}

	// Guard: if only the `did` field exists (no filterable properties), still
	// generate the WhereInput — `did` alone is useful.
	if len(fields) == 0 {
		return nil
	}

	fieldName := lexicon.ToFieldName(lex.ID)
	name := capitalize(fieldName) + "WhereInput"

	// Create the InputObject first, then add self-referential _and/_or via Thunk.
	whereInput := graphql.NewInputObject(graphql.InputObjectConfig{
		Name:        name,
		Description: fmt.Sprintf("Filter conditions for %s records. Field-level conditions are AND-composed. Use _and/_or for boolean composition (max depth %d).", lex.ID, repositories.MaxFilterDepth),
		Fields:      fields,
	})

	// Add _and and _or as self-referential fields using AddFieldConfig
	// (avoids Thunk complexity — AddFieldConfig resolves after type registration).
	whereInput.AddFieldConfig("_and", &graphql.InputObjectFieldConfig{
		Type:        graphql.NewList(whereInput),
		Description: "All conditions in this list must match (AND). Supports nesting.",
	})
	whereInput.AddFieldConfig("_or", &graphql.InputObjectFieldConfig{
		Type:        graphql.NewList(whereInput),
		Description: "At least one condition in this list must match (OR). Supports nesting.",
	})

	return whereInput
}

// propertyToFilterInput returns the appropriate GraphQL filter input type for
// a lexicon property. Returns nil if the property is not filterable.
func propertyToFilterInput(prop lexicon.Property) *graphql.InputObject {
	// Check format first (more specific than base type).
	if prop.Format != "" {
		input := types.FilterInputForLexiconType(prop.Format)
		if input != nil {
			return input
		}
	}
	return types.FilterInputForLexiconType(prop.Type)
}

// extractFieldFilters extracts a FilterGroup from a GraphQL `where` argument map.
// Supports recursive _and/_or composition with depth limiting.
func extractFieldFilters(whereArg interface{}, lex *lexicon.Lexicon) (repositories.FilterGroup, error) {
	return extractFieldFiltersRecursive(whereArg, lex, 0)
}

func extractFieldFiltersRecursive(whereArg interface{}, lex *lexicon.Lexicon, depth int) (repositories.FilterGroup, error) {
	whereMap, ok := whereArg.(map[string]interface{})
	if !ok {
		return repositories.FilterGroup{Operator: repositories.GroupAND}, nil
	}

	if depth > repositories.MaxFilterDepth {
		return repositories.FilterGroup{}, fmt.Errorf("filter nesting exceeds maximum depth of %d", repositories.MaxFilterDepth)
	}

	group := repositories.FilterGroup{Operator: repositories.GroupAND}

	for fieldName, filterInput := range whereMap {
		// Handle _and/_or composition.
		if fieldName == "_and" || fieldName == "_or" {
			list, ok := filterInput.([]interface{})
			if !ok {
				continue
			}
			childOp := repositories.GroupAND
			if fieldName == "_or" {
				childOp = repositories.GroupOR
			}
			childGroup := repositories.FilterGroup{Operator: childOp}
			for _, item := range list {
				subGroup, err := extractFieldFiltersRecursive(item, lex, depth+1)
				if err != nil {
					return repositories.FilterGroup{}, err
				}
				childGroup.Children = append(childGroup.Children, subGroup)
			}
			group.Children = append(group.Children, childGroup)
			continue
		}

		filterMap, ok := filterInput.(map[string]interface{})
		if !ok {
			continue
		}

		// Lexicon-specific filter? Dispatch through the registry. The
		// emitted FieldFilter carries the Kind set on the descriptor;
		// the SQL emitter dispatches on Kind in buildSingleFilter.
		if lex != nil {
			if descriptor, ok := lookupFilterDescriptor(lex.ID, fieldName); ok {
				f, err := buildDIDOnlyEqInFilter(descriptor, filterMap)
				if err != nil {
					return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
				}
				group.Filters = append(group.Filters, f)
				continue
			}
		}

		// Determine if this is a JSON field or a column.
		isJSON := fieldName != "did"
		lexiconType := ""
		if isJSON && lex != nil && lex.Defs.Main != nil {
			prop := lex.Defs.Main.GetProperty(fieldName)
			if prop != nil {
				lexiconType = effectiveType(prop)
			}
		}

		for opStr, value := range filterMap {
			op, isNullOp := parseOperator(opStr)
			if isNullOp {
				boolVal, ok := value.(bool)
				if !ok {
					continue
				}
				f := repositories.FieldFilter{
					FieldName:   fieldName,
					IsNull:      &boolVal,
					IsJSON:      isJSON,
					LexiconType: lexiconType,
				}
				if err := f.Validate(); err != nil {
					return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
				}
				group.Filters = append(group.Filters, f)
				continue
			}
			if op == "" {
				slog.Warn("Unknown filter operator", "field", fieldName, "op", opStr)
				continue
			}

			f := repositories.FieldFilter{
				FieldName:   fieldName,
				Operator:    op,
				Value:       value,
				IsJSON:      isJSON,
				LexiconType: lexiconType,
			}
			if err := f.Validate(); err != nil {
				return repositories.FilterGroup{}, fmt.Errorf("field %q, op %q: %w", fieldName, opStr, err)
			}
			group.Filters = append(group.Filters, f)
		}
	}

	return group, nil
}

// effectiveType returns the lexicon type to use for SQL CAST decisions.
// Prefers format over base type (e.g., a string with format "datetime"
// should use timestamptz cast).
func effectiveType(prop *lexicon.Property) string {
	if prop.Format != "" {
		return prop.Format
	}
	return prop.Type
}

// parseOperator maps a GraphQL operator string to a FilterOperator.
// Returns ("", false) for unknown operators, ("", true) for isNull.
func parseOperator(op string) (repositories.FilterOperator, bool) {
	switch op {
	case "isNull":
		return "", true
	case "eq":
		return repositories.OpEq, false
	case "neq":
		return repositories.OpNeq, false
	case "gt":
		return repositories.OpGt, false
	case "lt":
		return repositories.OpLt, false
	case "gte":
		return repositories.OpGte, false
	case "lte":
		return repositories.OpLte, false
	case "in":
		return repositories.OpIn, false
	case "contains":
		return repositories.OpContains, false
	case "startsWith":
		return repositories.OpStartsWith, false
	default:
		return "", false
	}
}

// capitalize returns the string with the first letter uppercased.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// buildDIDOnlyEqInFilter parses a DIDFilterInput-shaped input map
// (accepting `eq` and `in` only, every value strictly validated as a
// DID) and emits a FieldFilter whose Kind is set from the descriptor.
// This is the canonical builder for any registered lexicon-specific
// filter that takes a DID-only `eq`/`in` shape; the descriptor's Kind
// drives SQL emission downstream.
//
// Error messages mention the descriptor's GraphQL field name so
// validation errors surface the user-visible name (e.g. "contributor"
// or "subject") rather than an internal field path.
func buildDIDOnlyEqInFilter(descriptor filterDescriptor, filterMap map[string]interface{}) (repositories.FieldFilter, error) {
	if eqVal, ok := filterMap["eq"]; ok {
		s, ok := eqVal.(string)
		if !ok {
			return repositories.FieldFilter{}, fmt.Errorf("eq value must be a string DID, got %T", eqVal)
		}
		if !did.IsValid(s) {
			return repositories.FieldFilter{}, fmt.Errorf("%s filter values must be DIDs (did:...); handle values are not indexed as a queryable identity (rejected value: %q)", descriptor.FieldName, s)
		}
		return repositories.FieldFilter{
			FieldName: descriptor.FieldName,
			Operator:  repositories.OpEq,
			Value:     s,
			IsJSON:    true,
			Kind:      descriptor.Kind,
		}, nil
	}
	if inVal, ok := filterMap["in"]; ok {
		raw, ok := inVal.([]interface{})
		if !ok {
			return repositories.FieldFilter{}, fmt.Errorf("in value must be a list of DIDs, got %T", inVal)
		}
		if len(raw) == 0 {
			return repositories.FieldFilter{}, fmt.Errorf("%s in: list must contain at least one DID", descriptor.FieldName)
		}
		if len(raw) > repositories.MaxInListSize {
			return repositories.FieldFilter{}, fmt.Errorf("in list exceeds maximum of %d values", repositories.MaxInListSize)
		}
		values := make([]string, 0, len(raw))
		for i, item := range raw {
			s, ok := item.(string)
			if !ok {
				return repositories.FieldFilter{}, fmt.Errorf("in[%d] must be a string DID, got %T", i, item)
			}
			if !did.IsValid(s) {
				return repositories.FieldFilter{}, fmt.Errorf("%s filter values must be DIDs (did:...); handle values are not indexed as a queryable identity (rejected value: %q)", descriptor.FieldName, s)
			}
			values = append(values, s)
		}
		return repositories.FieldFilter{
			FieldName: descriptor.FieldName,
			Operator:  repositories.OpIn,
			Value:     values,
			IsJSON:    true,
			Kind:      descriptor.Kind,
		}, nil
	}
	return repositories.FieldFilter{}, fmt.Errorf("%s filter requires `eq` or `in`", descriptor.FieldName)
}
