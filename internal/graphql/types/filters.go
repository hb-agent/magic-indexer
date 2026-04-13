package types

import (
	"github.com/graphql-go/graphql"
)

// Filter input types for per-field filtering on lexicon-defined records.
// Each type provides operators appropriate for its data type.

// StringFilterInput provides string comparison operators.
var StringFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "StringFilterInput",
	Description: "Filter conditions for string fields",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq":         {Type: graphql.String, Description: "Equal to"},
		"neq":        {Type: graphql.String, Description: "Not equal to (includes records where field is absent)"},
		"in":         {Type: graphql.NewList(graphql.String), Description: "In list (max 50 values)"},
		"contains":   {Type: graphql.String, Description: "Contains substring (case-insensitive, min 3 chars)"},
		"startsWith": {Type: graphql.String, Description: "Starts with prefix (case-insensitive)"},
		"isNull":     {Type: graphql.Boolean, Description: "Is null / is not null"},
	},
})

// IntFilterInput provides integer comparison operators.
var IntFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "IntFilterInput",
	Description: "Filter conditions for integer fields",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq":     {Type: graphql.Int, Description: "Equal to"},
		"neq":    {Type: graphql.Int, Description: "Not equal to"},
		"gt":     {Type: graphql.Int, Description: "Greater than"},
		"lt":     {Type: graphql.Int, Description: "Less than"},
		"gte":    {Type: graphql.Int, Description: "Greater than or equal to"},
		"lte":    {Type: graphql.Int, Description: "Less than or equal to"},
		"in":     {Type: graphql.NewList(graphql.Int), Description: "In list (max 50 values)"},
		"isNull": {Type: graphql.Boolean, Description: "Is null / is not null"},
	},
})

// FloatFilterInput provides float comparison operators.
var FloatFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "FloatFilterInput",
	Description: "Filter conditions for number fields",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq":     {Type: graphql.Float, Description: "Equal to"},
		"neq":    {Type: graphql.Float, Description: "Not equal to"},
		"gt":     {Type: graphql.Float, Description: "Greater than"},
		"lt":     {Type: graphql.Float, Description: "Less than"},
		"gte":    {Type: graphql.Float, Description: "Greater than or equal to"},
		"lte":    {Type: graphql.Float, Description: "Less than or equal to"},
		"isNull": {Type: graphql.Boolean, Description: "Is null / is not null"},
	},
})

// BooleanFilterInput provides boolean comparison operators.
var BooleanFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "BooleanFilterInput",
	Description: "Filter conditions for boolean fields",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq":     {Type: graphql.Boolean, Description: "Equal to"},
		"isNull": {Type: graphql.Boolean, Description: "Is null / is not null"},
	},
})

// DateTimeFilterInput provides datetime comparison operators.
var DateTimeFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "DateTimeFilterInput",
	Description: "Filter conditions for datetime fields",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq":     {Type: graphql.String, Description: "Equal to (ISO-8601)"},
		"neq":    {Type: graphql.String, Description: "Not equal to"},
		"gt":     {Type: graphql.String, Description: "After (ISO-8601)"},
		"lt":     {Type: graphql.String, Description: "Before (ISO-8601)"},
		"gte":    {Type: graphql.String, Description: "At or after (ISO-8601)"},
		"lte":    {Type: graphql.String, Description: "At or before (ISO-8601)"},
		"isNull": {Type: graphql.Boolean, Description: "Is null / is not null"},
	},
})

// DIDFilterInput provides DID-specific operators (column-level, not JSON).
var DIDFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "DIDFilterInput",
	Description: "Filter conditions for DID fields (column-level, optimized)",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq": {Type: graphql.String, Description: "Equal to a specific DID"},
		"in": {Type: graphql.NewList(graphql.String), Description: "In list of DIDs (max 50)"},
	},
})

// FilterInputForLexiconType returns the appropriate filter input type for a
// lexicon property type. Returns nil if the type is not filterable.
func FilterInputForLexiconType(propType string) *graphql.InputObject {
	switch propType {
	case "string", "uri", "handle":
		return StringFilterInput
	case "did":
		return DIDFilterInput
	case "integer":
		return IntFilterInput
	case "number":
		return FloatFilterInput
	case "boolean":
		return BooleanFilterInput
	case "datetime":
		return DateTimeFilterInput
	case "at-uri", "tid":
		return StringFilterInput
	case "cid", "cid-link":
		// Restricted: only eq and in (no pattern matching on content hashes).
		return StringFilterInput // reuse StringFilterInput; unused ops are just ignored
	default:
		// Arrays, refs, unions, objects, blobs, bytes — not filterable.
		return nil
	}
}
