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

// graphFollowSubjectDescription is pinned verbatim so consumers see
// the policy at schema introspection.
const graphFollowSubjectDescription = `Filter follows by the subject DID — the account being followed. Use this to assemble a followers list: where: { subject: { eq: <did> } } returns every follow record pointing at that DID. The follower is the record author (filter via the did field, or read from node.did). DIDs only — handle values are rejected at the GraphQL layer. Compose with the did filter via _or to express "I follow OR am followed by": where: { _or: [ { did: { eq: "did:plc:me" } }, { subject: { eq: "did:plc:me" } } ] }.`

// badgeAwardBadgeDescription is the pinned description for the
// AppCertifiedBadgeAwardWhereInput.badge nested-where filter
// (issue #87). Visible at schema introspection.
const badgeAwardBadgeDescription = `Filter badge awards by properties of the joined badge definition record. Every field on AppCertifiedBadgeDefinitionWhereInput is available — most usefully badgeType (e.g. {eq: "endorsement"}) for the Endorsements-tab read pattern, but title / description / severity / did all work too. The filter resolves via an EXISTS subquery: only awards whose strongRef badge.uri resolves to a definition that matches the inner where are returned. An award pointing at a missing or deleted definition fails the existence check, so {badge: {}} (no inner filter) can be used to drop awards with broken refs. Compose with the outer subject + did filters via the usual _and (the default) or _or. Example, "endorsements OR verifications received by me": where: { subject: { eq: "did:plc:me" }, badge: { _or: [ { badgeType: { eq: "endorsement" } }, { badgeType: { eq: "verification" } } ] } }.`

// collectionItemsArrayDescription is the pinned description for
// the OrgHypercertsCollectionWhereInput.items array-element
// nested-where filter (issue #88). Visible at schema introspection.
const collectionItemsArrayDescription = `Filter collections to those whose items array contains at least one element matching the inner where (any-element semantics). Every property on the #item element def is filterable — itemIdentifier (a com.atproto.repo.strongRef, queryable via {uri: {eq: "at://..."}} or {cid: {eq: "..."}}) and itemWeight (string). Compose with the outer type / title / did filters via the usual _and (default) or _or, and inside the inner you can use _and/_or to mix item-element conditions. The filter resolves via an EXISTS subquery over jsonb_array_elements(json->'items'); a collection with an empty or absent items array fails the existence check, so {items: {}} (no inner predicate) filters to collections that have at least one item. Example, "project collections containing this cert": where: { type: { eqi: "project" }, items: { itemIdentifier: { uri: { eq: "at://did:plc:.../org.hypercerts.claim.activity/abc" } } } }.`

// joinedWhereDescriptor describes a strongRef-style filter that
// joins to records in a different collection.
//
// SECURITY: JoinExpr is emitted verbatim into SQL by the
// EXISTS-subquery builder in
// internal/database/repositories/filter.go. Registry values are
// code-defined and must NEVER source from request data — they
// form the SQL fragment for the EXISTS subquery's join
// predicate. Treat additions to this registry as a SQL diff.
type joinedWhereDescriptor struct {
	FieldName     string
	TargetLexicon string // the joined collection
	JoinExpr      string // SQL fragment extracting the referenced URI from the outer record
	Description   string // pinned consumer-facing text
}

// joinedWhereRegistry maps parentLexiconID → fieldName → descriptor
// for every nested-where field that joins to another collection.
//
// First entry (issue #87): app.certified.badge.award.badge joins
// to app.certified.badge.definition via the strongRef in
// award.json.badge.uri. Each award has exactly one badge ref;
// the EXISTS subquery returns awards whose joined definition
// matches the inner where.
var joinedWhereRegistry = map[string]map[string]joinedWhereDescriptor{
	"app.certified.badge.award": {
		"badge": {
			FieldName:     "badge",
			TargetLexicon: "app.certified.badge.definition",
			JoinExpr:      "r.json->'badge'->>'uri'",
			Description:   badgeAwardBadgeDescription,
		},
	},
}

// lookupJoinedWhereDescriptor returns the descriptor for a
// (lexicon, field) pair if the joined-where registry has one.
func lookupJoinedWhereDescriptor(lexID, fieldName string) (joinedWhereDescriptor, bool) {
	if byField, ok := joinedWhereRegistry[lexID]; ok {
		d, ok := byField[fieldName]
		return d, ok
	}
	return joinedWhereDescriptor{}, false
}

// arrayWhereDescriptor describes an array-element nested-where
// filter on a lexicon's array-of-objects property (issue #88).
//
// SECURITY: ArrayPath is emitted verbatim into SQL by the
// EXISTS-subquery builder in
// internal/database/repositories/filter.go. Registry values are
// code-defined and must NEVER source from request data — they
// form the SQL fragment for the EXISTS subquery's array path.
// Treat additions to this registry as a SQL diff. Same contract
// as joinedWhereDescriptor.JoinExpr.
type arrayWhereDescriptor struct {
	FieldName   string // the array property name on the outer record (e.g. "items")
	ArrayPath   string // SQL fragment yielding the jsonb array, e.g. "r.json->'items'"
	ElementDef  string // the local def name within the parent lexicon (e.g. "item")
	Description string // pinned consumer-facing text
}

// arrayWhereRegistry maps parentLexiconID → fieldName → descriptor
// for every array-element nested-where field.
//
// First entry (issue #88): org.hypercerts.collection.items, an
// array of #item objects, each with itemIdentifier:strongRef and
// optional itemWeight:string. Enables the certified-app's cross-
// DID "projects containing this cert" read in one round-trip.
var arrayWhereRegistry = map[string]map[string]arrayWhereDescriptor{
	"org.hypercerts.collection": {
		"items": {
			FieldName:   "items",
			ArrayPath:   "r.json->'items'",
			ElementDef:  "item",
			Description: collectionItemsArrayDescription,
		},
	},
}

// lookupArrayWhereDescriptor returns the descriptor for a
// (lexicon, field) pair if the array-where registry has one.
func lookupArrayWhereDescriptor(lexID, fieldName string) (arrayWhereDescriptor, bool) {
	if byField, ok := arrayWhereRegistry[lexID]; ok {
		d, ok := byField[fieldName]
		return d, ok
	}
	return arrayWhereDescriptor{}, false
}

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
	"app.certified.graph.follow": {
		"subject": {
			Kind:        repositories.KindStringSubject,
			FieldName:   "subject",
			Description: graphFollowSubjectDescription,
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
	//
	// The registry intentionally overrides any property-derived field
	// of the same name written by the loop above — for lexicons like
	// app.certified.graph.follow whose `subject` is also a filterable
	// scalar (`type: string, format: did`), the property loop writes a
	// DIDFilterInput first and the registry then replaces it with the
	// same DIDFilterInput plus the pinned description from the
	// registry. extractFieldFiltersRecursive does the same registry-
	// first routing, so the SQL path goes through the descriptor's
	// FilterKind, not the default scalar path. Do not reorder these
	// loops without revisiting both call sites.
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

// buildArrayElementInputType constructs the per-element WhereInput
// for a registered array-where descriptor (issue #88). The input
// name reuses the existing response-side element type with a
// WhereInput suffix — for org.hypercerts.collection.items the
// response type is OrgHypercertsCollectionItem so the input is
// OrgHypercertsCollectionItemWhereInput. Matches the
// <RecordType>WhereInput precedent (R2.3).
//
// Property dispatch:
//   - scalar properties (string / integer / datetime / etc.) route
//     through propertyToFilterInput.
//   - ref properties pointing at com.atproto.repo.strongRef get
//     StrongRefFilterInput (uri + cid as StringFilterInput).
//   - other ref / union / array / object properties are skipped
//     silently (not filterable in v1; follow-up §9.4 to relax).
//
// _and / _or are injected via AddFieldConfig at the bottom so the
// inner can use the same boolean composition as the outer.
//
// Returns nil if the parent lexicon does not contain the referenced
// element def — caller logs and skips the field injection.
func buildArrayElementInputType(parentLex *lexicon.Lexicon, descriptor arrayWhereDescriptor) *graphql.InputObject {
	elementDef, ok := parentLex.Defs.Others[descriptor.ElementDef]
	if !ok || !elementDef.IsObject() {
		return nil
	}

	fields := graphql.InputObjectConfigFieldMap{}
	for _, entry := range elementDef.Object.Properties {
		var filterType *graphql.InputObject
		switch {
		case entry.Property.Type == "ref" && entry.Property.Ref == "com.atproto.repo.strongRef":
			filterType = types.StrongRefFilterInput
		default:
			filterType = propertyToFilterInput(entry.Property)
		}
		if filterType == nil {
			continue
		}
		fields[entry.Name] = &graphql.InputObjectFieldConfig{
			Type:        filterType,
			Description: fmt.Sprintf("Filter by %s on each element", entry.Name),
		}
	}

	if len(fields) == 0 {
		return nil
	}

	parentFieldName := lexicon.ToFieldName(parentLex.ID)
	name := capitalize(parentFieldName) + capitalize(descriptor.ElementDef) + "WhereInput"

	input := graphql.NewInputObject(graphql.InputObjectConfig{
		Name:        name,
		Description: fmt.Sprintf("Filter conditions for a %s.%s element. Field-level conditions are AND-composed. Use _and/_or for boolean composition (max depth %d).", parentLex.ID, descriptor.ElementDef, repositories.MaxFilterDepth),
		Fields:      fields,
	})

	// Inject _and / _or after type registration so the inner can compose
	// the same way the outer WhereInput does.
	input.AddFieldConfig("_and", &graphql.InputObjectFieldConfig{
		Type:        graphql.NewList(input),
		Description: "All conditions in this list must match (AND). Supports nesting.",
	})
	input.AddFieldConfig("_or", &graphql.InputObjectFieldConfig{
		Type:        graphql.NewList(input),
		Description: "At least one condition in this list must match (OR). Supports nesting.",
	})

	return input
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
//
// The registry parameter is used by the joined-where branch
// (issue #87) to look up target lexicons by ID — the
// joinedWhereRegistry maps parent lexicons to target lexicons,
// and the extractor needs to recurse against the target's
// property/filter shape. Pass nil to disable joined-where
// extraction (extractor will not recognize joined fields).
func extractFieldFilters(whereArg interface{}, lex *lexicon.Lexicon, registry *lexicon.Registry) (repositories.FilterGroup, error) {
	return extractFieldFiltersRecursive(whereArg, lex, registry, 0)
}

func extractFieldFiltersRecursive(whereArg interface{}, lex *lexicon.Lexicon, registry *lexicon.Registry, depth int) (repositories.FilterGroup, error) {
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
				subGroup, err := extractFieldFiltersRecursive(item, lex, registry, depth+1)
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

		// Joined-where? Dispatch through the joined-where registry
		// (issue #87). The inner is recursively extracted against
		// the TARGET lexicon's property/filter shape, then wrapped
		// in a JoinedFilter that the SQL builder will emit as an
		// EXISTS subquery. Joined-where nesting is bounded to one
		// level — the inner must not itself contain Joined entries.
		if lex != nil && registry != nil {
			if jd, ok := lookupJoinedWhereDescriptor(lex.ID, fieldName); ok {
				targetLex, found := registry.GetLexicon(jd.TargetLexicon)
				if !found {
					return repositories.FilterGroup{}, fmt.Errorf(
						"field %q: target lexicon %q not registered", fieldName, jd.TargetLexicon)
				}
				inner, err := extractFieldFiltersRecursive(filterInput, targetLex, registry, depth+1)
				if err != nil {
					return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
				}
				if len(inner.Joined) > 0 {
					return repositories.FilterGroup{}, fmt.Errorf(
						"field %q: nested joined-where not supported (max one level)", fieldName)
				}
				group.Joined = append(group.Joined, repositories.JoinedFilter{
					TargetCollection: jd.TargetLexicon,
					JoinExpr:         jd.JoinExpr,
					Inner:            inner,
				})
				continue
			}
		}

		// Array-where? Dispatch through the array-where registry
		// (issue #88). The inner is recursively extracted against a
		// synthetic pseudo-lexicon whose Main RecordDef wraps the
		// parent's element ObjectDef, then wrapped in an ArrayFilter
		// that the SQL builder will emit as an EXISTS-over-
		// jsonb_array_elements subquery. One-level bound: the inner
		// must not itself contain Arrays.
		//
		// Branch precedence at this `for fieldName, ...` loop:
		// joined-where > array-where > filterRegistry > scalar fall-
		// through. Today no fieldName appears in more than one
		// registry for the same lexID, so the order is only
		// load-bearing for a future contributor adding a colliding
		// entry — the explicit ordering here makes that a deliberate
		// edit rather than an accident (IR1.7).
		if lex != nil {
			if ad, ok := lookupArrayWhereDescriptor(lex.ID, fieldName); ok {
				elemLex, err := makeElementPseudoLexicon(lex, ad)
				if err != nil {
					return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
				}
				inner, err := extractElementFilters(filterInput, elemLex, depth+1)
				if err != nil {
					return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
				}
				if len(inner.Arrays) > 0 {
					return repositories.FilterGroup{}, fmt.Errorf(
						"field %q: nested array-where not supported (max one level)", fieldName)
				}
				group.Arrays = append(group.Arrays, repositories.ArrayFilter{
					FieldName: ad.FieldName,
					ArrayPath: ad.ArrayPath,
					Inner:     inner,
				})
				continue
			}
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
	case "eqi":
		return repositories.OpEqi, false
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
	case "ini":
		return repositories.OpIni, false
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

// makeElementPseudoLexicon wraps the parent lexicon's element
// ObjectDef in a synthetic *lexicon.Lexicon whose Defs.Main is a
// RecordDef carrying the same property list. This lets the
// recursive extractor walk the inner where with the standard
// signature (which expects a parent record, not a bare object).
//
// The synthetic ID uses the `#`-anchor convention
// (`<parent>#<elementDef>`) because real lexicon IDs never
// contain `#` — this guarantees no lookup against
// filterRegistry / joinedWhereRegistry / arrayWhereRegistry under
// the pseudo-ID can collide with a real lexicon's entries.
//
// `RecordDef` and `ObjectDef` carry the same `[]PropertyEntry`
// field but are distinct types — the helper does a value-level
// rewrite, not pointer reuse. The `sync.Once` + `propIndex`
// private fields zero-init correctly and the lazy index is built
// the first time GetProperty is called on the synthetic
// RecordDef.
func makeElementPseudoLexicon(parent *lexicon.Lexicon, ad arrayWhereDescriptor) (*lexicon.Lexicon, error) {
	elemDef, ok := parent.Defs.Others[ad.ElementDef]
	if !ok || !elemDef.IsObject() {
		return nil, fmt.Errorf("element def %q not found or not an object on lexicon %q", ad.ElementDef, parent.ID)
	}
	return &lexicon.Lexicon{
		ID: parent.ID + "#" + ad.ElementDef,
		Defs: lexicon.Defs{
			Main: &lexicon.RecordDef{
				Type:       "object",
				Properties: elemDef.Object.Properties,
			},
		},
	}, nil
}

// extractElementFilters extracts the inner FilterGroup for an
// array-element nested-where (issue #88). Mirrors the body of
// extractFieldFiltersRecursive but with a critical addition:
// per-property dispatch runs BEFORE the existing scalar-operator
// loop so ref-property fields (`itemIdentifier:
// com.atproto.repo.strongRef`) get decomposed via
// extractStrongRefFilter rather than silently dropped by the
// scalar loop's `parseOperator("uri") -> ("", false)` path.
//
// The parent lexicon is forwarded so registry lookups inside the
// inner (joined-where, array-where) can still resolve — today
// none of the element-def properties trigger those branches, but
// the bound is "Inner.Arrays must be empty" enforced by the
// caller, not "no registries inside an array inner."
func extractElementFilters(whereArg interface{}, elemLex *lexicon.Lexicon, depth int) (repositories.FilterGroup, error) {
	whereMap, ok := whereArg.(map[string]interface{})
	if !ok {
		return repositories.FilterGroup{Operator: repositories.GroupAND}, nil
	}

	if depth > repositories.MaxFilterDepth {
		return repositories.FilterGroup{}, fmt.Errorf("filter nesting exceeds maximum depth of %d", repositories.MaxFilterDepth)
	}

	group := repositories.FilterGroup{Operator: repositories.GroupAND}

	// Cache the parent's element ObjectDef so per-property dispatch
	// can read each property's lexicon type. The pseudo-RecordDef
	// would also work, but the parent's Others map is the canonical
	// source — re-reading it keeps the dispatch logic obviously
	// keyed to the registry, not the synthetic shape.
	elementDef := elemLex.Defs.Main // synthetic RecordDef wrapping the parent's ObjectDef

	for fieldName, filterInput := range whereMap {
		// _and / _or composition (same shape as the outer extractor).
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
				subGroup, err := extractElementFilters(item, elemLex, depth+1)
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

		prop := elementDef.GetProperty(fieldName)
		if prop == nil {
			// Field not in element def — skip silently (mirrors the
			// outer extractor's behaviour for unknown fields).
			continue
		}

		// Ref dispatch BEFORE the scalar-operator loop. Without
		// this, {itemIdentifier: {uri: {eq: ...}}} would hit the
		// scalar loop at the `uri` key and warn-and-continue
		// (silent drop). See plan §5.5.1 + review-round-1 R1.4.
		if prop.Type == "ref" && prop.Ref == "com.atproto.repo.strongRef" {
			leaves, err := extractStrongRefFilter(fieldName, filterMap)
			if err != nil {
				return repositories.FilterGroup{}, fmt.Errorf("field %q: %w", fieldName, err)
			}
			group.Filters = append(group.Filters, leaves...)
			continue
		}

		// Scalar-operator dispatch (mirrors the outer extractor).
		lexiconType := effectiveType(prop)
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
					IsJSON:      true, // element rows: always JSON
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
				IsJSON:      true,
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

// extractStrongRefFilter decomposes a strongRef-shaped filter
// input ({uri: {eq: ...}, cid: {eq: ...}}) into one FieldFilter
// per sub-key using the `__`-nested-path convention that
// jsonExtract + buildNestedContainment already understand. The
// emitted FieldFilters have FieldName "<fieldName>__uri" /
// "<fieldName>__cid" and route through the scalar OpEq+IsJSON
// path which emits `<alias>.json @>
// '{"<fieldName>":{"uri":...}}'::jsonb` via the JSONB containment
// shape.
//
// Today only `eq` and `in` are routed; other operators
// (`contains`, `startsWith`, ...) on the sub-keys would also
// type-check but aren't useful for strongRefs (uri/cid are
// content-hashed identifiers, not search targets). Reject them
// with a clear error so a future contributor reading the GraphQL
// surface knows the intentional restriction.
func extractStrongRefFilter(fieldName string, filterMap map[string]interface{}) ([]repositories.FieldFilter, error) {
	var leaves []repositories.FieldFilter
	for subKey, subValue := range filterMap {
		if subKey != "uri" && subKey != "cid" {
			return nil, fmt.Errorf("strongRef filter on %q: unknown subfield %q (only uri and cid are supported)", fieldName, subKey)
		}
		subMap, ok := subValue.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("strongRef filter on %q.%q: expected an input object, got %T", fieldName, subKey, subValue)
		}
		path := fieldName + "__" + subKey
		for opStr, value := range subMap {
			op, isNullOp := parseOperator(opStr)
			if isNullOp {
				return nil, fmt.Errorf("strongRef filter on %q.%q: isNull is not supported (strongRef subfields are always present when the ref itself is)", fieldName, subKey)
			}
			if op != repositories.OpEq && op != repositories.OpIn {
				return nil, fmt.Errorf("strongRef filter on %q.%q: operator %q is not supported (use eq or in)", fieldName, subKey, opStr)
			}
			f := repositories.FieldFilter{
				FieldName:   path,
				Operator:    op,
				Value:       value,
				IsJSON:      true,
				LexiconType: "string",
			}
			if err := f.Validate(); err != nil {
				return nil, fmt.Errorf("field %q, op %q: %w", path, opStr, err)
			}
			leaves = append(leaves, f)
		}
	}
	return leaves, nil
}
