package repositories

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// FilterOperator represents a comparison operator for field filters.
type FilterOperator string

const (
	OpEq         FilterOperator = "eq"
	OpNeq        FilterOperator = "neq"
	OpGt         FilterOperator = "gt"
	OpLt         FilterOperator = "lt"
	OpGte        FilterOperator = "gte"
	OpLte        FilterOperator = "lte"
	OpIn         FilterOperator = "in"
	OpEqi        FilterOperator = "eqi"
	OpIni        FilterOperator = "ini"
	OpContains   FilterOperator = "contains"
	OpStartsWith FilterOperator = "startsWith"
)

// FilterKind selects the SQL-emission strategy for a FieldFilter. Most
// filters use KindScalar — the standard per-operator path against a
// single JSON property or column. Lexicon-specific shapes that need a
// bespoke EXISTS / union match opt into a non-default Kind; the registry
// in the GraphQL `where` layer (see internal/graphql/schema/where.go)
// pins which lexID+field pair maps to which Kind, and the SQL emitter in
// buildSingleFilter dispatches on Kind. Adding a new shape is two
// edits: a new constant here, a new arm in buildSingleFilter, and a
// new registry entry where.go-side.
type FilterKind int

const (
	// KindScalar is the default: standard FieldFilter operators against
	// a JSON property or column. FieldName/IsJSON/LexiconType all carry
	// real meaning.
	KindScalar FilterKind = iota
	// KindArrayContributor signals the indexable contributor-
	// identities SQL shape used by the
	// `OrgHypercertsClaimActivityWhereInput.contributor` filter.
	// FieldName is informational only on this path; the SQL
	// hardcodes the JSON path. The SQL emits
	// `record_contributor_identities(r.json) @> ARRAY[$N]::text[]`
	// (eq) or `&&` (in), which the partial GIN expression index in
	// migration 024 (`idx_record_contributor_identities`) serves
	// directly. The wrapper function is created in migration 023
	// (Postgres rejects inline subqueries in index expressions —
	// SQLSTATE 0A000). Matches contributor identities written
	// either as a bare string (lexicon-compliant) or as the
	// production-drift object shape
	// {"$type":"...","identity":"<DID>"}.
	KindArrayContributor
	// KindUnionSubject signals the indexable BadgeAward subject
	// filter on `AppCertifiedBadgeAwardWhereInput.subject` (issue
	// #65). The badge.award lexicon's `subject` is a union of
	// `app.certified.defs#did` (object `{did: "did:plc:..."}`),
	// `com.atproto.repo.strongRef` (object with `uri` starting with
	// `at://<did>/...`), and a defensive bare-string branch.
	// Migration 025 materializes the canonical DID for all three
	// shapes into a STORED generated column `record.subject_did`;
	// migration 026 adds a partial btree on it. The SQL emitted
	// here is just `r.subject_did = $N` / `= ANY($N::text[])` so
	// the planner can use that index directly.
	KindUnionSubject
	// KindStringSubject signals the indexable bare-DID-subject
	// filter on per-collection resolvers whose `subject` field is
	// a plain string (lexicon format: did). Currently used by
	// `AppCertifiedGraphFollowWhereInput.subject` (issue #86).
	// Backed by the partial expression index
	// `idx_record_follow_subject` in migration 029, keyed on
	// `(json->>'subject')` and partial-predicated on
	// `collection = 'app.certified.graph.follow'`. The SQL emitted
	// here is `r.json->>'subject' = $N` / `= ANY($N::text[])`; the
	// expression must match the migration's `(json->>'subject')`
	// byte-for-byte (modulo the `r.` alias) for the planner to
	// pick the partial index. The regression test
	// TestStringSubjectFilter_IndexExpressionMatchesMigration029
	// in filter_unit_test.go guards this coupling.
	//
	// Unlike KindUnionSubject this kind needs no generated column
	// (the extraction is trivially `->>`) and no IMMUTABLE wrapper
	// function (`->>` is IMMUTABLE on jsonb in Postgres 12+). It
	// can be reused for any future lexicon whose `subject` is a
	// bare DID — the FilterKind enum value carries no
	// lexicon-specific information.
	KindStringSubject
)

// FieldFilter describes a single filter condition on a record field.
type FieldFilter struct {
	// FieldName is the JSON property name (validated against fieldNameRegex).
	FieldName string
	// Operator is the comparison operator.
	Operator FilterOperator
	// Value is the comparison value. Must be compatible with LexiconType.
	// For OpIn, this must be a []interface{} or []string.
	Value interface{}
	// IsNull, when non-nil, filters for NULL/NOT NULL.
	IsNull *bool
	// IsJSON indicates whether this filter targets a JSON field (true) or
	// a direct table column (false, e.g. "did").
	IsJSON bool
	// LexiconType is the lexicon property type ("string", "integer", etc.)
	// used for SQL CAST decisions.
	LexiconType string
	// Kind selects the SQL-emission strategy; defaults to KindScalar.
	// Non-default Kinds are bespoke per-lexicon shapes registered in
	// the GraphQL where-layer registry; the SQL emitter dispatches on
	// Kind in buildSingleFilter.
	Kind FilterKind
}

// MaxArrayContributorScan caps the per-row contributor-array size
// the filter will consider. A record with a contributors array
// longer than this becomes invisible to the filter (fail-safe
// semantics). Mirrors notifications.MaxContributorsBeforeReject so
// a record the notifications layer rejects also fails to match the
// filter.
//
// Enforcement lives inside the migration-023 wrapper function
// `record_contributor_identities(jsonb)` (it returns NULL when
// the array is over-long), NOT in the filter SQL — keeping the
// indexable expression at the top level so the planner can match
// the migration-024 partial GIN index. Tests still reference this
// constant to assert the boundary contract; the value MUST stay
// in sync with the literal in 023's function body (changing it
// requires REINDEX CONCURRENTLY idx_record_contributor_identities).
const MaxArrayContributorScan = 200

const (
	// MaxFilterConditions is the maximum number of filter conditions per query.
	MaxFilterConditions = 20
	// MaxInListSize is the maximum number of values in an IN list.
	MaxInListSize = 50
	// MinContainsLength is the minimum length for the contains operator value.
	MinContainsLength = 3
	// MinStartsWithLength is the minimum length for startsWith operator value.
	MinStartsWithLength = 1
)

// SortDirection represents the sort order.
type SortDirection string

const (
	SortASC  SortDirection = "ASC"
	SortDESC SortDirection = "DESC"
)

// SortOption describes how to sort query results.
type SortOption struct {
	// Field is a lexicon property name or "indexed_at".
	Field string
	// Direction is ASC or DESC.
	Direction SortDirection
}

// IsDefault returns true if this is the default sort (indexed_at DESC).
func (s *SortOption) IsDefault() bool {
	return s == nil || (s.Field == "indexed_at" && s.Direction == SortDESC)
}

// BuildSortExpr returns the SQL expression for the sort field.
// Direct columns (indexed_at, uri, etc.) use the column name.
// JSON fields use json->>'fieldName'.
func (s *SortOption) BuildSortExpr() (string, error) {
	if s == nil || s.Field == "indexed_at" {
		return "indexed_at", nil
	}
	switch s.Field {
	case "uri", "did", "collection", "cid", "rkey":
		return s.Field, nil
	default:
		if err := ValidateFieldName(s.Field); err != nil {
			return "", err
		}
		return fmt.Sprintf("json->>'%s'", s.Field), nil
	}
}

// GroupOperator defines how filters within a group are combined.
type GroupOperator string

const (
	GroupAND GroupOperator = "AND"
	GroupOR  GroupOperator = "OR"
)

// FilterGroup represents a recursive tree of filter conditions.
// Field-level filters are leaf nodes; children are sub-groups.
// Joined entries apply an inner FilterGroup to records in a
// different collection via a strongRef-style URI lookup — see
// JoinedFilter for the SQL shape.
type FilterGroup struct {
	Operator GroupOperator
	Filters  []FieldFilter
	Children []FilterGroup
	Joined   []JoinedFilter
	Arrays   []ArrayFilter
}

// JoinedFilter applies an inner FilterGroup to records in a
// different collection, joined by a strongRef-style URI lookup.
// The SQL shape emitted by buildFilterGroupRecursive is:
//
//	EXISTS (
//	  SELECT 1 FROM record d
//	  WHERE d.collection = $N
//	    AND d.uri = <JoinExpr>            -- evaluated in the outer scope
//	    AND (<Inner with alias "d">)
//	)
//
// JoinExpr is the SQL fragment that extracts the referenced URI
// from the outer record's JSON. For badge.award → badge.definition
// it's `r.json->'badge'->>'uri'`. The outer alias must be `r` for
// today's call sites (no nested joins yet); a future need would
// generalise via the alias plumbing.
//
// SECURITY: JoinExpr is emitted verbatim into SQL. The values
// come from the joinedWhereRegistry in internal/graphql/schema/
// where.go — code-defined, never sourced from request data.
// Treat additions to that registry as a SQL diff.
//
// Joined-where nesting is bounded to one level: Inner.Joined
// must be empty (enforced by the schema-side extractor). Nested
// EXISTS subqueries inside an inner clause become planner-
// unfriendly fast, and no current client wants them.
type JoinedFilter struct {
	TargetCollection string
	JoinExpr         string
	Inner            FilterGroup
}

// ArrayFilter applies an inner FilterGroup to elements of a JSON
// array field on the outer record. Match semantics are "any element
// satisfies" — emitted as:
//
//	EXISTS (
//	  SELECT 1
//	  FROM jsonb_array_elements(
//	    CASE WHEN jsonb_typeof(<ArrayPath>) = 'array'
//	         THEN <ArrayPath>
//	         ELSE '[]'::jsonb END
//	  ) AS e(json)
//	  WHERE <Inner with alias "e">
//	)
//
// ArrayPath is the SQL fragment that returns the jsonb array from
// the outer record's JSON. For org.hypercerts.collection.items
// it's `r.json->'items'`. The outer alias must be `r` for today's
// call sites (no nested arrays inside joins yet).
//
// The CASE WHEN guard short-circuits to an empty array when the
// field is missing, null, or non-array — jsonb_array_elements
// raises SQLSTATE 22023 on non-array input, which would brick the
// whole query (it's evaluated row-by-row in the EXISTS). One
// corrupt record would otherwise take down the result set; the
// guard makes the path defensive without per-registry duplication.
//
// SECURITY: ArrayPath is emitted verbatim into SQL. Values come
// from arrayWhereRegistry in internal/graphql/schema/where.go —
// code-defined, never sourced from request data. Treat additions
// to that registry as a SQL diff. Same contract as
// JoinedFilter.JoinExpr.
//
// One-level bound: Inner.Arrays must be empty (enforced by the
// schema-side extractor). Nested EXISTS-over-array-elements is
// expressible but adds no value at bounded volumes, and the SQL
// planner has no way to share work across the two
// jsonb_array_elements calls.
type ArrayFilter struct {
	FieldName string
	ArrayPath string
	Inner     FilterGroup
}

// IsEmpty returns true if the group has no filters, no children,
// no joined filters, and no array filters.
func (g *FilterGroup) IsEmpty() bool {
	return len(g.Filters) == 0 && len(g.Children) == 0 &&
		len(g.Joined) == 0 && len(g.Arrays) == 0
}

// CountConditions returns the total number of leaf filter conditions
// across the entire tree, including any joined or array inner trees.
// The global MaxFilterConditions cap is enforced against this count,
// so joined / array inner leaves contribute to the parent's budget
// (otherwise a query could bypass the cap by hiding leaves inside
// the EXISTS subqueries).
func (g *FilterGroup) CountConditions() int {
	count := len(g.Filters)
	for i := range g.Children {
		count += g.Children[i].CountConditions()
	}
	for i := range g.Joined {
		count += g.Joined[i].Inner.CountConditions()
	}
	for i := range g.Arrays {
		count += g.Arrays[i].Inner.CountConditions()
	}
	return count
}

const (
	// MaxFilterDepth is the maximum nesting depth for _and/_or groups.
	MaxFilterDepth = 3
)

// BuildFilterGroupClause builds a SQL WHERE clause fragment from
// a FilterGroup tree. Returns the clause (without WHERE keyword),
// parameter values, and any error. paramOffset is the starting
// parameter number.
//
// Thin wrapper that defaults the table alias to "r" — the alias
// GetByCollectionFiltered uses for the outer record table. The
// joined-where path inside EXISTS calls
// buildFilterGroupClauseWithAlias directly with "d".
func BuildFilterGroupClause(group FilterGroup, paramOffset int) (string, []interface{}, error) {
	// Enforce global condition count (includes joined inner leaves
	// via FilterGroup.CountConditions).
	totalConditions := group.CountConditions()
	if totalConditions > MaxFilterConditions {
		return "", nil, fmt.Errorf("too many filter conditions (%d across all groups), maximum is %d",
			totalConditions, MaxFilterConditions)
	}

	return buildFilterGroupClauseWithAlias(group, paramOffset, "r")
}

// buildFilterGroupClauseWithAlias is the alias-aware version of
// BuildFilterGroupClause. Called by the public wrapper with "r"
// for outer clauses, and by the EXISTS-emission path with "d"
// for inner joined clauses.
func buildFilterGroupClauseWithAlias(group FilterGroup, paramOffset int, alias string) (string, []interface{}, error) {
	return buildFilterGroupRecursive(group, paramOffset, 0, alias)
}

func buildFilterGroupRecursive(group FilterGroup, paramIdx, depth int, alias string) (string, []interface{}, error) {
	if depth > MaxFilterDepth {
		return "", nil, fmt.Errorf("filter nesting exceeds maximum depth of %d", MaxFilterDepth)
	}

	if group.IsEmpty() {
		return "", nil, nil
	}

	var clauses []string
	var params []interface{}

	// Build leaf filter clauses.
	for _, f := range group.Filters {
		if f.IsNull != nil {
			if err := f.Validate(); err != nil {
				return "", nil, err
			}
			expr := jsonExtract(alias, f.FieldName, f.IsJSON)
			if *f.IsNull {
				clauses = append(clauses, expr+" IS NULL")
			} else {
				clauses = append(clauses, expr+" IS NOT NULL")
			}
			continue
		}

		// Run value-level validation here so the recursive path
		// matches `BuildFieldFilterClause`'s contract. Without this
		// pass, filters reaching the recursive builder skip checks
		// like the `in`/`ini` non-empty / size guards and the
		// `eqi`/`ini` IsJSON guard, which are only enforced when
		// the legacy non-recursive path is taken or when the
		// GraphQL parser runs `Validate()` per FieldFilter.
		if err := f.Validate(); err != nil {
			return "", nil, fmt.Errorf("invalid filter on field %q: %w", f.FieldName, err)
		}
		clause, newParams, nextIdx, err := buildSingleFilter(f, paramIdx, alias)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
		params = append(params, newParams...)
		paramIdx = nextIdx
	}

	// Build child group clauses recursively (same alias).
	for _, child := range group.Children {
		childClause, childParams, err := buildFilterGroupRecursive(child, paramIdx, depth+1, alias)
		if err != nil {
			return "", nil, err
		}
		if childClause != "" {
			clauses = append(clauses, "("+childClause+")")
			params = append(params, childParams...)
			paramIdx += len(childParams)
		}
	}

	// Build joined filters as EXISTS subqueries. The inner clause
	// uses alias "d" against `record d`; the JoinExpr lives in the
	// outer scope (the OUTER's alias) and correlates via d.uri =
	// <JoinExpr>. Depth budget for the inner subtree resets to 0 —
	// intentional and bounded by the one-level-joined guard the
	// extractor enforces on Inner.Joined.
	for _, j := range group.Joined {
		// Inner clause uses the joined alias "d".
		innerClause, innerParams, err := buildFilterGroupClauseWithAlias(j.Inner, paramIdx, "d")
		if err != nil {
			return "", nil, err
		}
		// Target-collection parameter follows the inner's parameters.
		collParamIdx := paramIdx + len(innerParams)
		collParam := fmt.Sprintf("$%d", collParamIdx)
		var existsClause string
		if innerClause == "" {
			existsClause = fmt.Sprintf(
				"EXISTS (SELECT 1 FROM record d WHERE d.collection = %s AND d.uri = %s)",
				collParam, j.JoinExpr)
		} else {
			existsClause = fmt.Sprintf(
				"EXISTS (SELECT 1 FROM record d WHERE d.collection = %s AND d.uri = %s AND (%s))",
				collParam, j.JoinExpr, innerClause)
		}
		clauses = append(clauses, existsClause)
		params = append(params, innerParams...)
		params = append(params, j.TargetCollection)
		paramIdx = collParamIdx + 1
	}

	// Build array filters as EXISTS subqueries over
	// jsonb_array_elements. The inner clause uses alias "e" against
	// the synthetic row `e(json)`; the array path lives in the outer
	// scope. The CASE WHEN jsonb_typeof = 'array' guard prevents a
	// non-array value on a single record from raising 22023 and
	// bricking the entire result set. Depth budget for the inner
	// subtree resets to 0 — intentional and bounded by the one-level
	// guard the extractor enforces on Inner.Arrays.
	for _, arr := range group.Arrays {
		innerClause, innerParams, err := buildFilterGroupClauseWithAlias(arr.Inner, paramIdx, "e")
		if err != nil {
			return "", nil, err
		}
		var existsClause string
		if innerClause == "" {
			existsClause = fmt.Sprintf(
				"EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(%s) = 'array' THEN %s ELSE '[]'::jsonb END) AS e(json))",
				arr.ArrayPath, arr.ArrayPath)
		} else {
			existsClause = fmt.Sprintf(
				"EXISTS (SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(%s) = 'array' THEN %s ELSE '[]'::jsonb END) AS e(json) WHERE %s)",
				arr.ArrayPath, arr.ArrayPath, innerClause)
		}
		clauses = append(clauses, existsClause)
		params = append(params, innerParams...)
		paramIdx += len(innerParams)
	}

	if len(clauses) == 0 {
		return "", nil, nil
	}

	// Single clause doesn't need parens or operator.
	if len(clauses) == 1 {
		return clauses[0], params, nil
	}

	joiner := " AND "
	if group.Operator == GroupOR {
		joiner = " OR "
	}

	return strings.Join(clauses, joiner), params, nil
}

// fieldSegmentRegex validates a single segment of a field path.
var fieldSegmentRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ValidateFieldName checks that a field name (or nested path with "__" separator)
// is safe for SQL use. Each segment must match [a-zA-Z_][a-zA-Z0-9_]*.
// Max 3 nesting levels to prevent deep JSON traversal.
func ValidateFieldName(name string) error {
	parts := strings.Split(name, "__")
	if len(parts) > 3 {
		return fmt.Errorf("field path %q exceeds maximum nesting depth of 3", name)
	}
	for _, part := range parts {
		if !fieldSegmentRegex.MatchString(part) {
			return fmt.Errorf("invalid field name segment %q in %q: must match [a-zA-Z_][a-zA-Z0-9_]*", part, name)
		}
	}
	return nil
}

// Validate checks that the filter is well-formed and its value is compatible
// with the declared lexicon type.
func (f *FieldFilter) Validate() error {
	if err := ValidateFieldName(f.FieldName); err != nil {
		return err
	}

	if f.IsNull != nil {
		return nil // isNull doesn't need a value
	}

	if f.Operator == OpContains {
		s, ok := f.Value.(string)
		if !ok {
			return fmt.Errorf("contains operator requires string value")
		}
		if len(s) < MinContainsLength {
			return fmt.Errorf("contains value must be at least %d characters", MinContainsLength)
		}
	}

	if f.Operator == OpStartsWith {
		s, ok := f.Value.(string)
		if !ok {
			return fmt.Errorf("startsWith operator requires string value")
		}
		if len(s) < MinStartsWithLength {
			return fmt.Errorf("startsWith value must be at least %d characters", MinStartsWithLength)
		}
	}

	if f.Operator == OpIn {
		if err := validateInListShape(f.Value, "in"); err != nil {
			return err
		}
	}

	if f.Operator == OpEqi {
		if _, ok := f.Value.(string); !ok {
			return fmt.Errorf("eqi operator requires string value")
		}
		// eqi targets JSON properties. The only column-level filter
		// today is `did`, which is routed through DIDFilterInput and
		// never picks up eqi. Defense in depth.
		if !f.IsJSON {
			return fmt.Errorf("eqi operator is not supported on column-level fields; use eq")
		}
	}

	if f.Operator == OpIni {
		if err := validateInListShape(f.Value, "ini"); err != nil {
			return err
		}
		if !f.IsJSON {
			return fmt.Errorf("ini operator is not supported on column-level fields; use in")
		}
	}

	// For eq with containment, reject non-scalar values.
	if f.Operator == OpEq && f.IsJSON {
		if _, ok := f.Value.(map[string]interface{}); ok {
			return fmt.Errorf("eq filter value must be a scalar (got object)")
		}
		if _, ok := f.Value.([]interface{}); ok {
			return fmt.Errorf("eq filter value must be a scalar (got array)")
		}
	}

	return nil
}

// validateInListShape enforces the size / scalar-element / non-empty
// contract shared by `in` and `ini`. opName is used in error messages
// so the consumer sees the GraphQL-visible operator name. Lower- and
// upper-bound messages share the "1 to N" phrasing so consumers
// parsing for the cap don't need two regexes.
func validateInListShape(value interface{}, opName string) error {
	switch v := value.(type) {
	case []interface{}:
		if len(v) < 1 || len(v) > MaxInListSize {
			return fmt.Errorf("%s list must contain 1 to %d values (got %d)", opName, MaxInListSize, len(v))
		}
		for _, item := range v {
			if _, ok := item.(map[string]interface{}); ok {
				return fmt.Errorf("%s list contains non-scalar value (object)", opName)
			}
			if _, ok := item.([]interface{}); ok {
				return fmt.Errorf("%s list contains non-scalar value (array)", opName)
			}
		}
	case []string:
		if len(v) < 1 || len(v) > MaxInListSize {
			return fmt.Errorf("%s list must contain 1 to %d values (got %d)", opName, MaxInListSize, len(v))
		}
	default:
		return fmt.Errorf("%s operator requires a list value", opName)
	}
	return nil
}

// BuildFieldFilterClause builds the WHERE clause fragment for a set of field filters.
// Returns the clause (without WHERE keyword), the parameter values, and any error.
// paramOffset is the starting parameter number (e.g., 3 means first param is $3).
//
// Legacy non-recursive path. Used by callers that don't compose
// _and/_or. Always emits against the outer record alias "r"; the
// joined-where path goes through buildFilterGroupClauseWithAlias.
func BuildFieldFilterClause(filters []FieldFilter, paramOffset int) (string, []interface{}, error) {
	if len(filters) > MaxFilterConditions {
		return "", nil, fmt.Errorf("too many filter conditions (%d), maximum is %d", len(filters), MaxFilterConditions)
	}

	var clauses []string
	var params []interface{}
	paramIdx := paramOffset
	const alias = "r"

	for _, f := range filters {
		if err := f.Validate(); err != nil {
			return "", nil, fmt.Errorf("invalid filter on field %q: %w", f.FieldName, err)
		}

		// Handle isNull separately.
		if f.IsNull != nil {
			expr := jsonExtract(alias, f.FieldName, f.IsJSON)
			if *f.IsNull {
				clauses = append(clauses, expr+" IS NULL")
			} else {
				clauses = append(clauses, expr+" IS NOT NULL")
			}
			continue
		}

		clause, newParams, nextIdx, err := buildSingleFilter(f, paramIdx, alias)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
		params = append(params, newParams...)
		paramIdx = nextIdx
	}

	return strings.Join(clauses, " AND "), params, nil
}

// buildSingleFilter dispatches a single FieldFilter to its SQL
// emitter. The alias parameter qualifies column references and
// JSON paths so this can be called for the outer record (alias
// "r"), an inner joined-record subquery (alias "d"), or an
// inner array-element subquery (alias "e").
//
// Sentinel: the lexicon-specific filter kinds
// (KindArrayContributor, KindUnionSubject, KindStringSubject)
// emit hardcoded "r."-prefixed SQL fragments and are only
// correct for the outer scope. Trying to use one inside an
// EXISTS subquery (alias "d" or "e") would silently emit SQL
// that references the wrong table or a non-existent column on
// the synthetic row. Reject with an error rather than emit.
// Today no joined-where target collection or array-element
// type has a field that would route through these kinds, but
// the sentinel future-proofs against an accidental registry
// edit.
func buildSingleFilter(f FieldFilter, paramIdx int, alias string) (string, []interface{}, int, error) {
	if err := ValidateFieldName(f.FieldName); err != nil {
		return "", nil, paramIdx, err
	}

	switch f.Kind {
	case KindArrayContributor, KindUnionSubject, KindStringSubject:
		if alias != "r" {
			return "", nil, paramIdx, fmt.Errorf(
				"lexicon-specific filter kind %v cannot be used inside a nested subquery (joined-where or array-element, alias %q); only the outer scope is supported",
				f.Kind, alias)
		}
		switch f.Kind {
		case KindArrayContributor:
			return buildContributorFilter(f, paramIdx)
		case KindUnionSubject:
			return buildBadgeAwardSubjectFilter(f, paramIdx)
		case KindStringSubject:
			return buildStringSubjectFilter(f, paramIdx)
		}
	}

	switch f.Operator {
	case OpEq:
		if f.IsJSON {
			// Use JSONB containment for GIN index support.
			// For nested paths (a__b__c), construct {"a":{"b":{"c": value}}}.
			//
			// The containment expression today references the outer
			// `record.json` column via bare `json`. When this path
			// emits inside an EXISTS subquery, bare `json` would be
			// ambiguous between the outer `r.json` and the inner
			// `d.json`. Qualify with the alias.
			param := fmt.Sprintf("$%d", paramIdx)
			clause := fmt.Sprintf("%s.json @> %s::jsonb", alias, param)
			containment := buildNestedContainment(f.FieldName, f.Value)
			jsonBytes, err := json.Marshal(containment)
			if err != nil {
				return "", nil, paramIdx, fmt.Errorf("failed to marshal eq containment: %w", err)
			}
			return clause, []interface{}{string(jsonBytes)}, paramIdx + 1, nil
		}
		param := fmt.Sprintf("$%d", paramIdx)
		return fmt.Sprintf("%s = %s", qualifyColumn(alias, f.FieldName), param), []interface{}{f.Value}, paramIdx + 1, nil

	case OpNeq:
		expr := jsonExtract(alias, f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)
		// Include records where field is absent (NULL).
		clause := fmt.Sprintf("(%s != %s OR %s IS NULL)", expr, param, expr)
		return clause, []interface{}{f.Value}, paramIdx + 1, nil

	case OpGt, OpLt, OpGte, OpLte:
		expr := jsonExtractTyped(alias, f.FieldName, f.LexiconType, f.IsJSON)
		op := sqlOp(f.Operator)
		param := fmt.Sprintf("$%d", paramIdx)
		return fmt.Sprintf("%s %s %s", expr, op, param), []interface{}{f.Value}, paramIdx + 1, nil

	case OpIn:
		expr := jsonExtract(alias, f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)

		// Check for NULL values in the list.
		values, hasNull := extractInValues(f.Value)
		if hasNull {
			clause := fmt.Sprintf("(%s = ANY(%s::text[]) OR %s IS NULL)", expr, param, expr)
			return clause, []interface{}{values}, paramIdx + 1, nil
		}
		clause := fmt.Sprintf("%s = ANY(%s::text[])", expr, param)
		return clause, []interface{}{values}, paramIdx + 1, nil

	case OpEqi:
		// ASCII-fold both sides. `COLLATE "C"` keeps lower() byte-based
		// and IMMUTABLE so the expression can serve as an index
		// expression in a follow-up migration if a single field
		// becomes a hot case-insensitive filter target. Non-ASCII
		// inputs pass through unchanged on both sides — symmetry is
		// preserved because Go's asciiToLower mirrors Postgres
		// `lower(... COLLATE "C")` byte-for-byte on ASCII and is the
		// identity elsewhere.
		expr := jsonExtract(alias, f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)
		s, _ := f.Value.(string)
		clause := fmt.Sprintf(`lower((%s) COLLATE "C") = %s`, expr, param)
		return clause, []interface{}{asciiToLower(s)}, paramIdx + 1, nil

	case OpIni:
		expr := jsonExtract(alias, f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)
		raw, hasNull := extractInValues(f.Value)
		values := make([]string, len(raw))
		for i, v := range raw {
			values[i] = asciiToLower(v)
		}
		if hasNull {
			clause := fmt.Sprintf(`(lower((%s) COLLATE "C") = ANY(%s::text[]) OR %s IS NULL)`, expr, param, expr)
			return clause, []interface{}{values}, paramIdx + 1, nil
		}
		clause := fmt.Sprintf(`lower((%s) COLLATE "C") = ANY(%s::text[])`, expr, param)
		return clause, []interface{}{values}, paramIdx + 1, nil

	case OpContains:
		expr := jsonExtract(alias, f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)
		escaped := escapeLike(f.Value.(string))
		clause := fmt.Sprintf("%s ILIKE '%%' || %s || '%%' ESCAPE '\\'", expr, param)
		return clause, []interface{}{escaped}, paramIdx + 1, nil

	case OpStartsWith:
		expr := jsonExtract(alias, f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)
		escaped := escapeLike(f.Value.(string))
		clause := fmt.Sprintf("%s ILIKE %s || '%%' ESCAPE '\\'", expr, param)
		return clause, []interface{}{escaped}, paramIdx + 1, nil

	default:
		return "", nil, paramIdx, fmt.Errorf("unsupported operator: %s", f.Operator)
	}
}

// jsonExtract returns the SQL expression for extracting a JSON field as text.
//
// Column-level (non-JSON) field references are qualified with
// the table alias because `GetByCollectionFiltered` LEFT JOINs
// `actor`, and the `did` column exists on both `record` and
// `actor`. Without qualification, Postgres raises `column
// reference "did" is ambiguous (SQLSTATE 42702)`. JSON-path
// references are also qualified with the alias so this can be
// called from inside an EXISTS subquery whose joined-record
// alias differs from the outer one (`d` vs `r`) — bare `json`
// would be ambiguous when both aliased records are in scope.
//
// alias is the table alias to prefix; today's call sites pass
// "r" (outer record) or "d" (inner joined-record under EXISTS).
func jsonExtract(alias, fieldName string, isJSON bool) string {
	if !isJSON {
		return qualifyColumn(alias, fieldName)
	}
	parts := strings.Split(fieldName, "__")
	if len(parts) == 1 {
		return fmt.Sprintf("%s.json->>'%s'", alias, fieldName)
	}
	// Nested path: <alias>.json->'a'->'b'->>'c' (last segment uses ->> for text extraction).
	var expr string
	for i, part := range parts {
		switch {
		case i == 0 && i == len(parts)-1:
			expr = fmt.Sprintf("%s.json->>'%s'", alias, part)
		case i == 0:
			expr = fmt.Sprintf("%s.json->'%s'", alias, part)
		case i == len(parts)-1:
			expr = fmt.Sprintf("%s->>'%s'", expr, part)
		default:
			expr = fmt.Sprintf("%s->'%s'", expr, part)
		}
	}
	return expr
}

// jsonExtractTyped returns the SQL expression with appropriate CAST for typed comparison.
// Supports nested paths using "__" separator. The alias parameter
// qualifies both the column-level (non-JSON) early-return AND
// the JSON-path extraction, so this is safe inside an EXISTS
// subquery against a different table alias.
func jsonExtractTyped(alias, fieldName, lexiconType string, isJSON bool) string {
	if !isJSON {
		// Pre-existing latent: today no column-level field routes
		// through gt/lt operators (only `did` is column-level and
		// it's eq-only via DIDFilterInput). The alias qualification
		// here is symmetric with jsonExtract's non-JSON branch so a
		// future column-level gt/lt won't silently emit an
		// unqualified column name. (R1.2 in plan-review round 1.)
		return qualifyColumn(alias, fieldName)
	}
	base := jsonExtract(alias, fieldName, true)
	switch lexiconType {
	case "integer", "number":
		// Guard against non-numeric values.
		return fmt.Sprintf("CASE WHEN %s ~ '^-?[0-9]' THEN (%s)::numeric END", base, base)
	case "datetime":
		return fmt.Sprintf("(%s)::timestamptz", base)
	default:
		return base
	}
}

func sqlOp(op FilterOperator) string {
	switch op {
	case OpGt:
		return ">"
	case OpLt:
		return "<"
	case OpGte:
		return ">="
	case OpLte:
		return "<="
	default:
		return "="
	}
}

// qualifyColumn prefixes a column-level field reference with the
// supplied table alias. Today's call sites pass "r" (the outer
// record alias used by `GetByCollectionFiltered`) or "d" (the
// inner joined-record alias used inside an EXISTS subquery).
// See jsonExtract for the disambiguation rationale.
func qualifyColumn(alias, name string) string {
	return alias + "." + name
}

// buildContributorFilter emits the contributor-identities containment
// clause used by the KindArrayContributor filter on
// `org.hypercerts.claim.activity` records.
//
// SQL shape — `record_contributor_identities(r.json) @> ARRAY[$N]::text[]`
// (eq) and `&&` (in). `record_contributor_identities(jsonb)` is the
// IMMUTABLE SQL function defined in migration 023; the partial GIN
// expression index in migration 024 is keyed on the same function
// call, so the planner picks it for both operators (P-2 in the
// 2026-05-13 audit). Without the index match, the previous EXISTS
// shape was O(collection-size) per row and could trip the 5s
// /graphql budget on rare contributors in busy collections.
//
// Why a wrapper function rather than an inline ARRAY-subquery?
// Postgres rejects subqueries in index expressions
// (`cannot use subquery in index expression`, SQLSTATE 0A000), so
// the indexed form has to live inside an IMMUTABLE function. The
// function disambiguates the contributor-identity union with CASE
// on `jsonb_typeof` — bare-string (lexicon-compliant) on the
// `'string'` arm; the production-drift `{"$type":"...","identity":
// "<DID>"}` object on the `'object'` arm. (A naive COALESCE would
// pick `c->>'contributorIdentity'` first, which on an object
// returns the object's JSON-text serialisation rather than NULL —
// so the indexed text[] would contain the wrong bytes.)
//
// Safety: the wrapper function returns NULL for non-array
// `contributors` AND for arrays longer than
// notifications.MaxContributorsBeforeReject (cap synced to the
// 200 baked into migration 023). `NULL @> ARRAY[$N]` is NULL,
// which evaluates to FALSE in WHERE, so no outer guard is needed
// in the filter SQL — the indexable expression stands alone at
// the top level for the planner to match against the partial
// index. This is the trick that lets the GIN index be used: any
// outer CASE wrapper would hide the indexable expression and
// silently degrade the filter back to O(collection-size).
func buildContributorFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
	// indexedExpr MUST match the index expression in migration 024
	// byte-for-byte (modulo the `r.` alias prefix on `json`) for the
	// planner to recognise it as indexable. A diff here silently
	// degrades the filter back to O(collection-size).
	const indexedExpr = `record_contributor_identities(r.json)`

	switch f.Operator {
	case OpEq:
		param := fmt.Sprintf("$%d", paramIdx)
		clause := fmt.Sprintf("(%s @> ARRAY[%s]::text[])", indexedExpr, param)
		return clause, []interface{}{f.Value}, paramIdx + 1, nil
	case OpIn:
		param := fmt.Sprintf("$%d", paramIdx)
		values, _ := extractInValues(f.Value)
		clause := fmt.Sprintf("(%s && %s::text[])", indexedExpr, param)
		return clause, []interface{}{values}, paramIdx + 1, nil
	default:
		return "", nil, paramIdx, fmt.Errorf("operator %s not supported on contributor filter", f.Operator)
	}
}

// buildBadgeAwardSubjectFilter handles the `subject` filter on
// app.certified.badge.award (issue #65). The `subject` field is a
// union of two lexicon refs (plus a defensive bare-string branch):
//
//   - app.certified.defs#did     → object `{"did": "did:plc:abc"}`
//   - com.atproto.repo.strongRef → object `{"uri": "at://did:plc:abc/.../...", "cid": "..."}`
//   - bare string `at://did:plc:abc/...` (defensive — no production records)
//
// SQL shape — direct equality / `= ANY(...)` against the
// `r.subject_did` generated column created by migration 025. The
// column's expression covers all three subject shapes (CASE on
// `jsonb_typeof(json->'subject')` + COALESCE over `did` / extracted
// URI prefix), and migration 026 creates the partial btree
// `idx_record_subject_did` scoped to `app.certified.badge.award`
// that the planner uses for both `=` and `= ANY(...)`. This
// replaces the previous `LIKE 'at://' || $N || '/%'` pattern, which
// the planner cannot index against because the LIKE pattern is
// parameter-driven (P-3 in the 2026-05-13 audit). Without an index,
// a rare DID on a busy badge.award collection could trip the 5s
// /graphql budget.
//
// The schema layer validates the input DID before it gets here, so
// the column comparison is exact-match against a known-good DID
// shape — no `at://`-prefix LIKE pattern needed.
//
// Note: this is single-value-OR-list, NOT array-of-subjects. Each
// record has exactly one subject; we match it against one or many
// candidate DIDs.
func buildBadgeAwardSubjectFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
	switch f.Operator {
	case OpEq:
		param := fmt.Sprintf("$%d", paramIdx)
		return fmt.Sprintf("r.subject_did = %s", param), []interface{}{f.Value}, paramIdx + 1, nil
	case OpIn:
		param := fmt.Sprintf("$%d", paramIdx)
		values, _ := extractInValues(f.Value)
		return fmt.Sprintf("r.subject_did = ANY(%s::text[])", param), []interface{}{values}, paramIdx + 1, nil
	default:
		return "", nil, paramIdx, fmt.Errorf("operator %s not supported on badge-award subject filter", f.Operator)
	}
}

// buildStringSubjectFilter handles the `subject` filter on
// per-collection resolvers whose `subject` field is a bare DID
// string (lexicon format: did). First user: the
// `AppCertifiedGraphFollowWhereInput.subject` filter (issue #86).
//
// The expression `r.json->>'subject'` must match the partial
// expression index `idx_record_follow_subject` from migration 029
// byte-for-byte (modulo the `r.` alias). A drift here silently
// degrades the filter to a sequential scan over the follow
// collection, which trips the 5s /graphql budget at scale. The
// regression test
// TestStringSubjectFilter_IndexExpressionMatchesMigration029 in
// filter_unit_test.go pins the coupling.
//
// The collection predicate is supplied by the resolver's outer
// WHERE (`r.collection = $coll`); the planner matches it to the
// migration's partial-index predicate
// `WHERE collection = 'app.certified.graph.follow'` via
// Postgres's `predicate_implied_by`. Same mechanism as
// idx_record_subject_did (#026) on badge.award.
func buildStringSubjectFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
	// indexedExpr MUST match the index expression in migration 029
	// byte-for-byte (modulo the `r.` alias). A diff here silently
	// degrades the filter to a sequential scan.
	const indexedExpr = `r.json->>'subject'`

	switch f.Operator {
	case OpEq:
		param := fmt.Sprintf("$%d", paramIdx)
		return fmt.Sprintf("%s = %s", indexedExpr, param), []interface{}{f.Value}, paramIdx + 1, nil
	case OpIn:
		param := fmt.Sprintf("$%d", paramIdx)
		// hasNull (second return) is intentionally discarded —
		// the DIDFilterInput schema validation upstream rejects
		// null list entries before the SQL layer sees them.
		// Mirrors the same pattern in buildContributorFilter and
		// buildBadgeAwardSubjectFilter.
		values, _ := extractInValues(f.Value)
		return fmt.Sprintf("%s = ANY(%s::text[])", indexedExpr, param), []interface{}{values}, paramIdx + 1, nil
	default:
		return "", nil, paramIdx, fmt.Errorf("operator %s not supported on string-subject filter", f.Operator)
	}
}

// buildNestedContainment constructs a nested JSON object for @> containment.
// For "a__b__c" with value "x", returns {"a": {"b": {"c": "x"}}}.
// For "field" with value "x", returns {"field": "x"}.
func buildNestedContainment(fieldName string, value interface{}) interface{} {
	parts := strings.Split(fieldName, "__")
	if len(parts) == 1 {
		return map[string]interface{}{fieldName: value}
	}
	// Build from inside out.
	result := value
	for i := len(parts) - 1; i >= 0; i-- {
		result = map[string]interface{}{parts[i]: result}
	}
	return result
}

// escapeLike escapes special characters for LIKE/ILIKE with ESCAPE '\'.
// Order matters: escape backslash first, then % and _.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

// asciiToLower folds ASCII A-Z to a-z and leaves all other bytes
// (including non-ASCII bytes that may be parts of multi-byte
// UTF-8 sequences) untouched. This mirrors Postgres
// `lower(... COLLATE "C")` byte-for-byte so the case-insensitive
// `eqi` / `ini` operators stay symmetric between the bound
// parameter and the column expression. Note this is deliberately
// NOT `strings.ToLower`: Go's default ToLower applies Unicode
// mappings (e.g. Turkish `İ` → `i̇`), which would diverge from
// the ASCII-only fold Postgres performs under `COLLATE "C"`.
func asciiToLower(s string) string {
	var changed bool
	for i := 0; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' {
			changed = true
			break
		}
	}
	if !changed {
		return s
	}
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// extractInValues extracts string values from an IN list, reporting whether
// any NULL values were present.
func extractInValues(v interface{}) ([]string, bool) {
	var values []string
	var hasNull bool

	switch list := v.(type) {
	case []interface{}:
		for _, item := range list {
			if item == nil {
				hasNull = true
				continue
			}
			values = append(values, fmt.Sprintf("%v", item))
		}
	case []string:
		values = list
	}
	return values, hasNull
}
