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
