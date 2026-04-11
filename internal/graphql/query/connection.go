// Package query provides GraphQL query type building.
package query

import (
	"github.com/graphql-go/graphql"
)

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

// ConnectionArgs returns standard Relay connection arguments for forward pagination,
// plus label-based filtering arguments used by record collection queries.
func ConnectionArgs() graphql.FieldConfigArgument {
	args := graphql.FieldConfigArgument{
		"first": &graphql.ArgumentConfig{
			Type:        graphql.Int,
			Description: "Number of items to return (default 20)",
		},
		"after": &graphql.ArgumentConfig{
			Type:        graphql.String,
			Description: "Cursor to start after (forward pagination)",
		},
	}
	for k, v := range LabelFilterArgs() {
		args[k] = v
	}
	return args
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
