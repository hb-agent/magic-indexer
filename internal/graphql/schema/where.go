package schema

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/graphql/types"
	"github.com/GainForest/hypergoat/internal/lexicon"
)

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

	// Guard: if only the `did` field exists (no filterable properties), still
	// generate the WhereInput — `did` alone is useful.
	if len(fields) == 0 {
		return nil
	}

	fieldName := lexicon.ToFieldName(lex.ID)
	name := capitalize(fieldName) + "WhereInput"

	return graphql.NewInputObject(graphql.InputObjectConfig{
		Name:        name,
		Description: fmt.Sprintf("Filter conditions for %s records. All conditions are combined with AND.", lex.ID),
		Fields:      fields,
	})
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

// extractFieldFilters extracts FieldFilter conditions from a GraphQL `where`
// argument map. The lexicon is used to determine property types for SQL CAST.
func extractFieldFilters(whereArg interface{}, lex *lexicon.Lexicon) ([]repositories.FieldFilter, error) {
	whereMap, ok := whereArg.(map[string]interface{})
	if !ok {
		return nil, nil
	}

	var filters []repositories.FieldFilter

	for fieldName, filterInput := range whereMap {
		filterMap, ok := filterInput.(map[string]interface{})
		if !ok {
			continue
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
					return nil, fmt.Errorf("field %q: %w", fieldName, err)
				}
				filters = append(filters, f)
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
				return nil, fmt.Errorf("field %q, op %q: %w", fieldName, opStr, err)
			}
			filters = append(filters, f)
		}
	}

	if len(filters) > repositories.MaxFilterConditions {
		return nil, fmt.Errorf("too many filter conditions (%d), maximum is %d",
			len(filters), repositories.MaxFilterConditions)
	}

	return filters, nil
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
