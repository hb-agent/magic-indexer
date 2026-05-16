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
type FilterGroup struct {
	Operator GroupOperator
	Filters  []FieldFilter
	Children []FilterGroup
}

// IsEmpty returns true if the group has no filters and no children.
func (g *FilterGroup) IsEmpty() bool {
	return len(g.Filters) == 0 && len(g.Children) == 0
}

// CountConditions returns the total number of leaf filter conditions
// across the entire tree.
func (g *FilterGroup) CountConditions() int {
	count := len(g.Filters)
	for i := range g.Children {
		count += g.Children[i].CountConditions()
	}
	return count
}

const (
	// MaxFilterDepth is the maximum nesting depth for _and/_or groups.
	MaxFilterDepth = 3
)

// BuildFilterGroupClause builds a SQL WHERE clause fragment from a FilterGroup tree.
// Returns the clause (without WHERE keyword), parameter values, and any error.
// paramOffset is the starting parameter number.
func BuildFilterGroupClause(group FilterGroup, paramOffset int) (string, []interface{}, error) {
	// Enforce global condition count.
	totalConditions := group.CountConditions()
	if totalConditions > MaxFilterConditions {
		return "", nil, fmt.Errorf("too many filter conditions (%d across all groups), maximum is %d",
			totalConditions, MaxFilterConditions)
	}

	return buildFilterGroupRecursive(group, paramOffset, 0)
}

func buildFilterGroupRecursive(group FilterGroup, paramIdx, depth int) (string, []interface{}, error) {
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
			expr := jsonExtract(f.FieldName, f.IsJSON)
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
		clause, newParams, nextIdx, err := buildSingleFilter(f, paramIdx)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
		params = append(params, newParams...)
		paramIdx = nextIdx
	}

	// Build child group clauses recursively.
	for _, child := range group.Children {
		childClause, childParams, err := buildFilterGroupRecursive(child, paramIdx, depth+1)
		if err != nil {
			return "", nil, err
		}
		if childClause != "" {
			clauses = append(clauses, "("+childClause+")")
			params = append(params, childParams...)
			paramIdx += len(childParams)
		}
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
// so the consumer sees the GraphQL-visible operator name.
func validateInListShape(value interface{}, opName string) error {
	switch v := value.(type) {
	case []interface{}:
		if len(v) == 0 {
			return fmt.Errorf("%s list must contain at least 1 value (max %d)", opName, MaxInListSize)
		}
		if len(v) > MaxInListSize {
			return fmt.Errorf("%s list exceeds maximum of %d values", opName, MaxInListSize)
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
		if len(v) == 0 {
			return fmt.Errorf("%s list must contain at least 1 value (max %d)", opName, MaxInListSize)
		}
		if len(v) > MaxInListSize {
			return fmt.Errorf("%s list exceeds maximum of %d values", opName, MaxInListSize)
		}
	default:
		return fmt.Errorf("%s operator requires a list value", opName)
	}
	return nil
}

// BuildFieldFilterClause builds the WHERE clause fragment for a set of field filters.
// Returns the clause (without WHERE keyword), the parameter values, and any error.
// paramOffset is the starting parameter number (e.g., 3 means first param is $3).
func BuildFieldFilterClause(filters []FieldFilter, paramOffset int) (string, []interface{}, error) {
	if len(filters) > MaxFilterConditions {
		return "", nil, fmt.Errorf("too many filter conditions (%d), maximum is %d", len(filters), MaxFilterConditions)
	}

	var clauses []string
	var params []interface{}
	paramIdx := paramOffset

	for _, f := range filters {
		if err := f.Validate(); err != nil {
			return "", nil, fmt.Errorf("invalid filter on field %q: %w", f.FieldName, err)
		}

		// Handle isNull separately.
		if f.IsNull != nil {
			expr := jsonExtract(f.FieldName, f.IsJSON)
			if *f.IsNull {
				clauses = append(clauses, expr+" IS NULL")
			} else {
				clauses = append(clauses, expr+" IS NOT NULL")
			}
			continue
		}

		clause, newParams, nextIdx, err := buildSingleFilter(f, paramIdx)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, clause)
		params = append(params, newParams...)
		paramIdx = nextIdx
	}

	return strings.Join(clauses, " AND "), params, nil
}

func buildSingleFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
	if err := ValidateFieldName(f.FieldName); err != nil {
		return "", nil, paramIdx, err
	}

	switch f.Kind {
	case KindArrayContributor:
		return buildContributorFilter(f, paramIdx)
	case KindUnionSubject:
		return buildBadgeAwardSubjectFilter(f, paramIdx)
	}

	switch f.Operator {
	case OpEq:
		if f.IsJSON {
			// Use JSONB containment for GIN index support.
			// For nested paths (a__b__c), construct {"a":{"b":{"c": value}}}.
			param := fmt.Sprintf("$%d", paramIdx)
			clause := fmt.Sprintf("json @> %s::jsonb", param)
			containment := buildNestedContainment(f.FieldName, f.Value)
			jsonBytes, err := json.Marshal(containment)
			if err != nil {
				return "", nil, paramIdx, fmt.Errorf("failed to marshal eq containment: %w", err)
			}
			return clause, []interface{}{string(jsonBytes)}, paramIdx + 1, nil
		}
		param := fmt.Sprintf("$%d", paramIdx)
		return fmt.Sprintf("%s = %s", qualifyColumn(f.FieldName), param), []interface{}{f.Value}, paramIdx + 1, nil

	case OpNeq:
		expr := jsonExtract(f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)
		// Include records where field is absent (NULL).
		clause := fmt.Sprintf("(%s != %s OR %s IS NULL)", expr, param, expr)
		return clause, []interface{}{f.Value}, paramIdx + 1, nil

	case OpGt, OpLt, OpGte, OpLte:
		expr := jsonExtractTyped(f.FieldName, f.LexiconType, f.IsJSON)
		op := sqlOp(f.Operator)
		param := fmt.Sprintf("$%d", paramIdx)
		return fmt.Sprintf("%s %s %s", expr, op, param), []interface{}{f.Value}, paramIdx + 1, nil

	case OpIn:
		expr := jsonExtract(f.FieldName, f.IsJSON)
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
		expr := jsonExtract(f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)
		s, _ := f.Value.(string)
		clause := fmt.Sprintf(`lower((%s) COLLATE "C") = %s`, expr, param)
		return clause, []interface{}{asciiToLower(s)}, paramIdx + 1, nil

	case OpIni:
		expr := jsonExtract(f.FieldName, f.IsJSON)
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
		expr := jsonExtract(f.FieldName, f.IsJSON)
		param := fmt.Sprintf("$%d", paramIdx)
		escaped := escapeLike(f.Value.(string))
		clause := fmt.Sprintf("%s ILIKE '%%' || %s || '%%' ESCAPE '\\'", expr, param)
		return clause, []interface{}{escaped}, paramIdx + 1, nil

	case OpStartsWith:
		expr := jsonExtract(f.FieldName, f.IsJSON)
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
// Column-level (non-JSON) field references are qualified with the
// `r.` alias because `GetByCollectionFiltered` LEFT JOINs `actor`,
// and the `did` column exists on both `record` and `actor`. Without
// qualification, Postgres raises `column reference "did" is
// ambiguous (SQLSTATE 42702)`. The only column-level field today is
// `did`; adding more would require either a per-field qualification
// map or assuming every column lives on `r`. Keeping it explicit.
func jsonExtract(fieldName string, isJSON bool) string {
	if !isJSON {
		return qualifyColumn(fieldName)
	}
	parts := strings.Split(fieldName, "__")
	if len(parts) == 1 {
		return fmt.Sprintf("json->>'%s'", fieldName)
	}
	// Nested path: json->'a'->'b'->>'c' (last segment uses ->> for text extraction).
	var expr string
	for i, part := range parts {
		switch {
		case i == 0 && i == len(parts)-1:
			expr = fmt.Sprintf("json->>'%s'", part)
		case i == 0:
			expr = fmt.Sprintf("json->'%s'", part)
		case i == len(parts)-1:
			expr = fmt.Sprintf("%s->>'%s'", expr, part)
		default:
			expr = fmt.Sprintf("%s->'%s'", expr, part)
		}
	}
	return expr
}

// jsonExtractTyped returns the SQL expression with appropriate CAST for typed comparison.
// Supports nested paths using "__" separator.
func jsonExtractTyped(fieldName, lexiconType string, isJSON bool) string {
	if !isJSON {
		return fieldName
	}
	base := jsonExtract(fieldName, true)
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
// `r.` alias used by `GetByCollectionFiltered`'s FROM clause. See
// jsonExtract for the disambiguation rationale.
func qualifyColumn(name string) string {
	return "r." + name
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
