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
}

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

var fieldNameRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// ValidateFieldName checks that a field name is safe for SQL use.
func ValidateFieldName(name string) error {
	if !fieldNameRegex.MatchString(name) {
		return fmt.Errorf("invalid field name %q: must match [a-zA-Z_][a-zA-Z0-9_]*", name)
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

	switch f.Operator {
	case OpEq:
		if f.IsJSON {
			// Use JSONB containment for GIN index support.
			// Construct {"fieldName": value} as a JSON string parameter.
			param := fmt.Sprintf("$%d", paramIdx)
			clause := fmt.Sprintf("json @> %s::jsonb", param)
			containment := map[string]interface{}{f.FieldName: f.Value}
			jsonBytes, err := json.Marshal(containment)
			if err != nil {
				return "", nil, paramIdx, fmt.Errorf("failed to marshal eq containment: %w", err)
			}
			return clause, []interface{}{string(jsonBytes)}, paramIdx + 1, nil
		}
		param := fmt.Sprintf("$%d", paramIdx)
		return fmt.Sprintf("%s = %s", f.FieldName, param), []interface{}{f.Value}, paramIdx + 1, nil

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
func jsonExtract(fieldName string, isJSON bool) string {
	if !isJSON {
		return fieldName
	}
	return fmt.Sprintf("json->>'%s'", fieldName)
}

// jsonExtractTyped returns the SQL expression with appropriate CAST for typed comparison.
func jsonExtractTyped(fieldName, lexiconType string, isJSON bool) string {
	if !isJSON {
		return fieldName
	}
	base := fmt.Sprintf("json->>'%s'", fieldName)
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
