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
	OpContains   FilterOperator = "contains"
	OpStartsWith FilterOperator = "startsWith"
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
	// IsArrayContributor, when true, signals the special EXISTS-over-
	// `json->'contributors'` SQL shape used by the
	// `OrgHypercertsClaimActivityWhereInput.contributor` filter.
	// FieldName is informational only on this path; the SQL hardcodes
	// the JSON path. The marker is intentionally narrow — if a second
	// collection adopts the same array-of-objects-with-identity shape,
	// rename to a Kind enum at that time.
	IsArrayContributor bool

	// IsBadgeAwardSubject, when true, signals the special "match
	// against subject-as-DID-string OR subject-as-strongRef-uri" SQL
	// shape used by the `AppCertifiedBadgeAwardWhereInput.subject`
	// filter (issue #65). The badge.award lexicon's `subject` is a
	// union of `app.certified.defs#did` (bare DID string) and
	// `com.atproto.repo.strongRef` (object with `uri` starting with
	// `at://<did>/...`). FieldName is informational; SQL hardcodes
	// the JSON path. Narrow marker like IsArrayContributor.
	IsBadgeAwardSubject bool
}

// MaxArrayContributorScan caps the per-row scan of the contributors
// array inside the contributor-filter EXISTS subquery. A record with
// a contributors array longer than this becomes invisible to the
// filter (fail-safe semantics). Mirrors
// notifications.MaxContributorsBeforeReject so a record the
// notifications layer rejects also fails to match the filter.
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
		switch v := f.Value.(type) {
		case []interface{}:
			if len(v) > MaxInListSize {
				return fmt.Errorf("in list exceeds maximum of %d values", MaxInListSize)
			}
			// Reject non-scalar values (objects/arrays).
			for _, item := range v {
				if _, ok := item.(map[string]interface{}); ok {
					return fmt.Errorf("in list contains non-scalar value (object)")
				}
				if _, ok := item.([]interface{}); ok {
					return fmt.Errorf("in list contains non-scalar value (array)")
				}
			}
		case []string:
			if len(v) > MaxInListSize {
				return fmt.Errorf("in list exceeds maximum of %d values", MaxInListSize)
			}
		default:
			return fmt.Errorf("in operator requires a list value")
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

	if f.IsArrayContributor {
		return buildContributorFilter(f, paramIdx)
	}

	if f.IsBadgeAwardSubject {
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

// buildContributorFilter emits the special EXISTS-over-
// `json->'contributors'` clause for the activity-contributor filter.
//
// The clause is wrapped in a CASE expression because Postgres does
// not guarantee left-to-right evaluation of AND operands in WHERE.
// Without CASE, the planner is free to invoke jsonb_array_length or
// jsonb_array_elements before the jsonb_typeof guard — both raise
// on a non-array operand, so a single stored record whose
// `contributors` is mis-shaped would brick every filtered query.
// CASE is the documented escape hatch for forcing evaluation order
// (https://www.postgresql.org/docs/current/sql-expressions.html).
//
// COALESCE-of-both-shapes lets the same SQL match contributor
// identities written either as a bare string (lexicon-compliant)
// or as the production-drift object shape
// {"$type":"...","identity":"<DID>"}.
func buildContributorFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
	// CASE-per-element to disambiguate the contributor-identity union.
	// `c->>'contributorIdentity'` on a JSON object returns the object's
	// JSON-text serialisation rather than NULL, which would defeat a
	// COALESCE between the two shapes. jsonb_typeof gates the access
	// so we only read each shape when the data actually matches.
	const candidateExpr = `CASE jsonb_typeof(c->'contributorIdentity') WHEN 'string' THEN c->>'contributorIdentity' WHEN 'object' THEN c->'contributorIdentity'->>'identity' END`
	tmplEq := `(CASE WHEN jsonb_typeof(r.json->'contributors') = 'array' AND jsonb_array_length(r.json->'contributors') <= %d THEN EXISTS (SELECT 1 FROM jsonb_array_elements(r.json->'contributors') AS c WHERE (` + candidateExpr + `) = %s) ELSE FALSE END)`
	tmplIn := `(CASE WHEN jsonb_typeof(r.json->'contributors') = 'array' AND jsonb_array_length(r.json->'contributors') <= %d THEN EXISTS (SELECT 1 FROM jsonb_array_elements(r.json->'contributors') AS c WHERE (` + candidateExpr + `) = ANY(%s::text[])) ELSE FALSE END)`

	switch f.Operator {
	case OpEq:
		param := fmt.Sprintf("$%d", paramIdx)
		clause := fmt.Sprintf(tmplEq, MaxArrayContributorScan, param)
		return clause, []interface{}{f.Value}, paramIdx + 1, nil
	case OpIn:
		param := fmt.Sprintf("$%d", paramIdx)
		values, _ := extractInValues(f.Value)
		clause := fmt.Sprintf(tmplIn, MaxArrayContributorScan, param)
		return clause, []interface{}{values}, paramIdx + 1, nil
	default:
		return "", nil, paramIdx, fmt.Errorf("operator %s not supported on contributor filter", f.Operator)
	}
}

// buildBadgeAwardSubjectFilter handles the special SQL shape for the
// `subject` filter on app.certified.badge.award (issue #65). The
// `subject` field is a union of two lexicon refs:
//
//   - app.certified.defs#did  → object `{"did": "did:plc:abc"}`
//   - com.atproto.repo.strongRef → object `{"uri": "at://did:plc:abc/.../...", "cid": "..."}`
//
// Production data confirms `subject` is ALWAYS an object — the
// defs#did ref resolves to an object-with-`did`-property, not a bare
// string. (PR #75 originally assumed a bare-string branch existed and
// missed 70% of records; this revision adds the defs#did object
// branch.)
//
// We match either by:
//
//   - Direct string equality on `json->'subject'->>'did'`  (defs#did)
//   - Prefix match on `json->'subject'->>'uri'` against `at://<did>/`
//     (strongRef)
//
// The legacy bare-string branch is kept defensively in case a producer
// writes that shape; production data has none today.
//
// Caller has already DID-validated the input; the LIKE pattern uses
// the trailing `/` to ensure we only match URIs whose first path
// segment is exactly the DID (rules out `at://did:plc:foo-something/...`
// matching `did:plc:foo`).
//
// Note: this is single-value-OR-list, NOT array-of-subjects. Each
// record has exactly one subject; we match it against one or many
// candidate DIDs.
func buildBadgeAwardSubjectFilter(f FieldFilter, paramIdx int) (string, []interface{}, int, error) {
	// jsonb_typeof + CASE per branch keeps the matcher predictable
	// across every shape we've observed in the wild. Object-with-`did`
	// and object-with-`uri` share the `WHEN 'object'` arm because their
	// `did` / `uri` extractions are independent — at most one yields a
	// non-null per row.
	const subjectStringExpr = `CASE jsonb_typeof(r.json->'subject') WHEN 'string' THEN r.json->>'subject' WHEN 'object' THEN r.json->'subject'->>'did' ELSE NULL END`
	const subjectURIExpr = `CASE jsonb_typeof(r.json->'subject') WHEN 'object' THEN r.json->'subject'->>'uri' ELSE NULL END`

	switch f.Operator {
	case OpEq:
		didParam := fmt.Sprintf("$%d", paramIdx)
		// LIKE pattern is `at://<did>/%` — concatenated server-side so
		// the user-supplied DID stays parameterised (no SQL injection
		// surface). DID was validated by the schema layer.
		clause := fmt.Sprintf(
			"((%s) = %s OR (%s) LIKE 'at://' || %s || '/%%')",
			subjectStringExpr, didParam, subjectURIExpr, didParam,
		)
		return clause, []interface{}{f.Value}, paramIdx + 1, nil
	case OpIn:
		didsParam := fmt.Sprintf("$%d", paramIdx)
		values, _ := extractInValues(f.Value)
		// For the IN case: string-equals-ANY OR strong-ref-uri-LIKE-ANY.
		// The LIKE-ANY half uses an unnested subquery so each candidate
		// DID generates its own `at://<did>/%` pattern.
		clause := fmt.Sprintf(
			"((%s) = ANY(%s::text[]) OR EXISTS (SELECT 1 FROM unnest(%s::text[]) AS d WHERE (%s) LIKE 'at://' || d || '/%%'))",
			subjectStringExpr, didsParam, didsParam, subjectURIExpr,
		)
		return clause, []interface{}{values}, paramIdx + 1, nil
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
