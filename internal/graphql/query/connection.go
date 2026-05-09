// Package query provides GraphQL query type building.
package query

import (
	"fmt"
	"strings"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/database/repositories"
)

const (
	// DefaultPageSize is the number of records returned when no `first`
	// argument is provided.
	DefaultPageSize = 20

	// MaxPageSize is the maximum number of records that can be requested
	// in a single page. Larger requests are clamped to this value; it
	// protects against a client asking for `first: 100000` and causing
	// the DB to build a response proportional to that request.
	MaxPageSize = 100
)

// ClampPageSize returns a valid page size within [1, MaxPageSize],
// defaulting to DefaultPageSize when a non-positive value is provided.
// Adopted from hypercerts-org/hyperindex#34.
func ClampPageSize(first int) int {
	if first <= 0 {
		return DefaultPageSize
	}
	if first > MaxPageSize {
		return MaxPageSize
	}
	return first
}

// PageInfoType defines the Relay-style pagination info GraphQL type.
var PageInfoType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "PageInfo",
	Description: "Information about pagination in a connection",
	Fields: graphql.Fields{
		"hasNextPage": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Boolean),
			Description: "Whether there are more items after the last edge",
		},
		"hasPreviousPage": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Boolean),
			Description: "Whether there are more items before the first edge",
		},
		"startCursor": &graphql.Field{
			Type:        graphql.String,
			Description: "Cursor of the first edge",
		},
		"endCursor": &graphql.Field{
			Type:        graphql.String,
			Description: "Cursor of the last edge",
		},
	},
})

// LabelFilterArgs returns the label-based filtering arguments applied to
// record connection queries. Exposed separately so the generic `records`
// query (which defines its own `collection` argument) can compose them
// alongside its existing args.
//
// The indexer is neutral about which labeler is authoritative: by default
// label filters match assertions from any labeler the indexer has
// ingested. Clients that want to scope to a specific trust set pass
// `labelerDids: ["did:plc:...", ...]`.
func LabelFilterArgs() graphql.FieldConfigArgument {
	return graphql.FieldConfigArgument{
		"labels": &graphql.ArgumentConfig{
			Type:        graphql.NewList(graphql.NewNonNull(graphql.String)),
			Description: "Filter to records that have at least one of these active labels. By default any labeler's labels match; scope to a trust set via labelerDids.",
		},
		"excludeLabels": &graphql.ArgumentConfig{
			Type:        graphql.NewList(graphql.NewNonNull(graphql.String)),
			Description: "Exclude records that have any of these active labels. By default any labeler's labels match; scope to a trust set via labelerDids.",
		},
		"labelerDids": &graphql.ArgumentConfig{
			Type:        graphql.NewList(graphql.NewNonNull(graphql.String)),
			Description: "Optional list of labeler DIDs to restrict label-based filtering to. When empty, labels from every configured labeler are considered.",
		},
	}
}

// PDSFilterArgs returns the PDS-based filtering arguments applied to
// record connection queries. Exposed separately so consumers (the
// generic `records` query, per-collection queries) can compose it
// alongside their other args.
//
// The filter is best-effort: records whose author's PDS has not yet
// been resolved (NULL on the actor row) pass through. Use AT-Protocol
// labels with excludeLabels for guaranteed exclusion semantics.
func PDSFilterArgs() graphql.FieldConfigArgument {
	return graphql.FieldConfigArgument{
		"excludePds": &graphql.ArgumentConfig{
			Type:        graphql.NewList(graphql.NewNonNull(graphql.String)),
			Description: "Exclude records authored from any of these PDS service endpoints. Records whose author's PDS has not yet been resolved pass through (best-effort exclusion). Use the labeler-based excludeLabels for guaranteed exclusion. Endpoint strings are matched verbatim (e.g. \"https://pds1.test.certified.app\").",
		},
	}
}

// SortDirectionEnum defines ASC/DESC for ordering.
var SortDirectionEnum = graphql.NewEnum(graphql.EnumConfig{
	Name:        "SortDirection",
	Description: "Sort direction",
	Values: graphql.EnumValueConfigMap{
		"ASC": &graphql.EnumValueConfig{
			Value:       "ASC",
			Description: "Ascending order",
		},
		"DESC": &graphql.EnumValueConfig{
			Value:       "DESC",
			Description: "Descending order (default)",
		},
	},
})

// ConnectionArgs returns standard Relay connection arguments for forward and backward
// pagination, plus label-based and author-based filtering arguments used by record
// collection queries.
func ConnectionArgs() graphql.FieldConfigArgument {
	args := graphql.FieldConfigArgument{
		"first": &graphql.ArgumentConfig{
			Type:        graphql.Int,
			Description: "Number of items to return (default 20, max 100)",
		},
		"after": &graphql.ArgumentConfig{
			Type:        graphql.String,
			Description: "Cursor to start after (forward pagination)",
		},
		"last": &graphql.ArgumentConfig{
			Type:        graphql.Int,
			Description: "Number of items to return from the end (backward pagination, max 100)",
		},
		"before": &graphql.ArgumentConfig{
			Type:        graphql.String,
			Description: "Cursor to start before (backward pagination)",
		},
		"orderBy": &graphql.ArgumentConfig{
			Type:         graphql.String,
			DefaultValue: "indexed_at",
			Description:  "Field to sort by (default: indexed_at). Use field names from the record schema.",
		},
		"orderDirection": &graphql.ArgumentConfig{
			Type:         SortDirectionEnum,
			DefaultValue: "DESC",
			Description:  "Sort direction (default: DESC)",
		},
		"search": &graphql.ArgumentConfig{
			Type:        graphql.String,
			Description: "Full-text search across title, shortDescription, description, and workScope. Terms are space-separated and implicitly ANDed. Uses English stemming.",
		},
		"authors": &graphql.ArgumentConfig{
			Type: graphql.NewList(graphql.NewNonNull(graphql.String)),
			Description: fmt.Sprintf(
				"Filter to records authored by (published under) any of these DIDs. "+
					"Passing an empty list returns zero results; passing null or omitting "+
					"the arg applies no filter. Maximum %d DIDs per query. "+
					"Duplicates are deduplicated server-side; order is not significant. "+
					"DIDs are case-sensitive per the ATProto spec.",
				repositories.MaxAuthorsFilterSize,
			),
		},
	}
	for k, v := range LabelFilterArgs() {
		args[k] = v
	}
	for k, v := range PDSFilterArgs() {
		args[k] = v
	}
	return args
}

// ParsePDSExcludeFilter extracts the "excludePds" argument from GraphQL
// resolver args. Returns nil for omitted/null/empty arguments (no filter)
// and a deduplicated, order-stable slice of endpoint strings otherwise.
// Returns an error on malformed input (non-string elements).
func ParsePDSExcludeFilter(args map[string]interface{}) ([]string, error) {
	raw, present := args["excludePds"]
	if !present || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("excludePds argument must be a list")
	}
	if len(list) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(list))
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("excludePds elements must be strings")
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out, nil
}

// ParseAuthorsFilter extracts the "authors" argument from GraphQL resolver args.
// Returns:
//
//	(nil,  nil) — argument omitted or explicitly null → no author filter
//	(&[],  nil) — empty list → explicit "match nothing" signal
//	(&[…], nil) — non-empty list → filter by these DIDs
//	(nil,  err) — malformed (e.g. non-string elements, or exceeds cap)
//
// The pointer-to-slice return type is load-bearing: the nil/empty distinction
// is the primary semantic difference and cannot be represented with a plain
// slice return.
func ParseAuthorsFilter(args map[string]interface{}) (*[]string, error) {
	raw, present := args["authors"]
	if !present || raw == nil {
		return nil, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, fmt.Errorf("authors argument must be a list")
	}
	dids := make([]string, 0, len(list))
	for _, e := range list {
		s, ok := e.(string)
		if !ok {
			return nil, fmt.Errorf("authors elements must be strings")
		}
		dids = append(dids, s)
	}
	if len(dids) > repositories.MaxAuthorsFilterSize {
		return nil, fmt.Errorf("authors filter exceeds maximum of %d DIDs", repositories.MaxAuthorsFilterSize)
	}
	return &dids, nil
}

// ParseSearchFilter extracts the "search" argument from GraphQL resolver args.
func ParseSearchFilter(args map[string]interface{}) string {
	raw, ok := args["search"].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(raw)
}

// BuildEdgeType creates an Edge type for a given node type.
func BuildEdgeType(nodeType *graphql.Object) *graphql.Object {
	return graphql.NewObject(graphql.ObjectConfig{
		Name:        nodeType.Name() + "Edge",
		Description: "An edge in a " + nodeType.Name() + " connection",
		Fields: graphql.Fields{
			"cursor": &graphql.Field{
				Type:        graphql.NewNonNull(graphql.String),
				Description: "Cursor for this edge",
			},
			"node": &graphql.Field{
				Type:        graphql.NewNonNull(nodeType),
				Description: "The item at the end of the edge",
			},
		},
	})
}

// BuildConnectionType creates a Connection type for a given node type.
func BuildConnectionType(nodeType *graphql.Object) *graphql.Object {
	edgeType := BuildEdgeType(nodeType)

	return graphql.NewObject(graphql.ObjectConfig{
		Name:        nodeType.Name() + "Connection",
		Description: "A paginated list of " + nodeType.Name() + " items",
		Fields: graphql.Fields{
			"edges": &graphql.Field{
				Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(edgeType))),
				Description: "List of edges",
			},
			"pageInfo": &graphql.Field{
				Type:        graphql.NewNonNull(PageInfoType),
				Description: "Pagination information",
			},
			"totalCount": &graphql.Field{
				Type:        graphql.Int,
				Description: "Total number of items (if known)",
			},
		},
	})
}
