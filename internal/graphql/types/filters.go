package types //nolint:revive // package name is descriptive within graphql context

import (
	"github.com/graphql-go/graphql"
)

// Filter input types for per-field filtering on lexicon-defined records.
// Each type provides operators appropriate for its data type.

// StringFilterInput provides string comparison operators. Operators
// suffixed with `i` are case-insensitive variants (ASCII fold via
// Postgres `lower(... COLLATE "C")`); operators without the suffix
// are case-sensitive. This `-i` suffix is the going-forward
// convention for case-insensitive operator variants on this filter
// input — future additions follow the same shape.
var StringFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "StringFilterInput",
	Description: "Filter conditions for string fields. Operators with an `-i` suffix are case-insensitive (ASCII fold); operators without are case-sensitive.",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq":         {Type: graphql.String, Description: "Equal to (case-sensitive)"},
		"eqi":        {Type: graphql.String, Description: stringEqiDescription},
		"neq":        {Type: graphql.String, Description: "Not equal to (includes records where field is absent)"},
		"in":         {Type: graphql.NewList(graphql.String), Description: "In list (case-sensitive, 1-50 values)"},
		"ini":        {Type: graphql.NewList(graphql.String), Description: stringIniDescription},
		"contains":   {Type: graphql.String, Description: "Contains substring (case-insensitive, min 3 chars)"},
		"startsWith": {Type: graphql.String, Description: "Starts with prefix (case-insensitive)"},
		"isNull":     {Type: graphql.Boolean, Description: "Is null / is not null"},
	},
})

const stringEqiDescription = `Equal to (case-insensitive, ASCII fold via Postgres lower(... COLLATE "C")). Both sides are lower-cased before comparison; non-ASCII characters pass through unchanged (no Unicode confusable folding). On its own, eqi does not use the JSONB GIN index — pair it with a column-level filter such as did { eq: ... } for selective queries. For spec-case-sensitive identifiers (cid, cid-link, lowercase TIDs, DID authorities inside at-uri values) prefer eq for the cheaper GIN-indexable comparison; eqi still evaluates correctly but provides no semantic benefit on those types.`

const stringIniDescription = `In list (case-insensitive, ASCII fold via Postgres lower(... COLLATE "C"); 1-50 values). Both sides are lower-cased; same non-ASCII and indexing caveats as eqi. An empty list is rejected.`

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
// Uses DateTimeScalar for ISO-8601 validation at the GraphQL layer; malformed
// datetimes are rejected at query parse instead of at SQL cast.
var DateTimeFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "DateTimeFilterInput",
	Description: "Filter conditions for datetime fields",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq":     {Type: DateTimeScalar, Description: "Equal to (ISO-8601)"},
		"neq":    {Type: DateTimeScalar, Description: "Not equal to"},
		"gt":     {Type: DateTimeScalar, Description: "After (ISO-8601)"},
		"lt":     {Type: DateTimeScalar, Description: "Before (ISO-8601)"},
		"gte":    {Type: DateTimeScalar, Description: "At or after (ISO-8601)"},
		"lte":    {Type: DateTimeScalar, Description: "At or before (ISO-8601)"},
		"isNull": {Type: graphql.Boolean, Description: "Is null / is not null"},
	},
})

// DIDFilterInput provides DID-specific operators (column-level, not JSON).
var DIDFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "DIDFilterInput",
	Description: "Filter conditions for DID fields (column-level, optimized). DIDs are spec-case-sensitive; no case-insensitive operators are provided on this filter input (per W3C DID Core §3.1 — case folding would change identifier identity).",
	Fields: graphql.InputObjectConfigFieldMap{
		"eq": {Type: graphql.String, Description: "Equal to a specific DID"},
		"in": {Type: graphql.NewList(graphql.String), Description: "In list of DIDs (max 50)"},
	},
})

// StrongRefFilterInput provides per-subfield filtering on a
// com.atproto.repo.strongRef shape ({uri, cid}). Both subfields
// route through StringFilterInput. Currently used only inside
// array-element WhereInputs (e.g. OrgHypercertsCollectionItem
// WhereInput.itemIdentifier); see follow-up §9.8 in
// docs/issue-88/plan.md for the dispatch-from-top-level path
// when a record property of strongRef type wants direct
// filtering.
var StrongRefFilterInput = graphql.NewInputObject(graphql.InputObjectConfig{
	Name:        "StrongRefFilterInput",
	Description: "Filter conditions for a com.atproto.repo.strongRef value ({uri, cid}). Equality on uri is the load-bearing case — it matches a specific record identity, regardless of which version (cid) is currently referenced. Equality on cid additionally pins the exact version.",
	Fields: graphql.InputObjectConfigFieldMap{
		"uri": {Type: StringFilterInput, Description: "Filter by the strongRef's uri (at://...)."},
		"cid": {Type: StringFilterInput, Description: "Filter by the strongRef's cid (content hash)."},
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
		// Content hashes reuse StringFilterInput. eq / in are the
		// natural operators; eqi / contains / startsWith are
		// semantically redundant (the value is opaque base32/base58),
		// but documented in the operator descriptions rather than
		// refused at validation — the precedent set by
		// contains/startsWith is that operator restrictions are
		// per-operator, not per-type.
		return StringFilterInput
	default:
		// Arrays, refs, unions, objects, blobs, bytes — not filterable.
		return nil
	}
}
