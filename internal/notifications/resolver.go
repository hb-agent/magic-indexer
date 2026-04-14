package notifications

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/graphql-go/graphql"
)

// Resolver provides GraphQL resolvers for the notifications subsystem.
type Resolver struct {
	repo *Repository
}

// NewResolver creates a new Resolver.
func NewResolver(repo *Repository) *Resolver {
	return &Resolver{repo: repo}
}

// notificationContextKey is the graphql context key for the isRead watermark.
type notificationContextKey struct{}

// watermarkFromContext retrieves the lastSeenNotifs time stashed on the context.
func watermarkFromContext(ctx context.Context) time.Time {
	if v, ok := ctx.Value(notificationContextKey{}).(time.Time); ok {
		return v
	}
	return time.Time{}
}

// ListNotifications is the resolver for `Query.notifications(did, reasons, first, after)`.
func (r *Resolver) ListNotifications(ctx context.Context, did string, reasons []string, first int, after string) (map[string]interface{}, error) {
	if did == "" {
		return nil, fmt.Errorf("did is required")
	}
	result, err := r.repo.List(ctx, did, reasons, first, after)
	if err != nil {
		return nil, err
	}

	watermark := result.LastSeenNotifs

	edges := make([]map[string]interface{}, 0, len(result.Rows))
	var startCursor, endCursor string
	for _, row := range result.Rows {
		cursor := encodeCursor(row.SortAt, row.ID)
		if startCursor == "" {
			startCursor = cursor
		}
		endCursor = cursor
		node := map[string]interface{}{
			"id":              fmt.Sprintf("%d", row.ID),
			"reason":          row.Reason,
			"reasonSubject":   nonEmptyOrNil(row.ReasonSubject),
			"sortAt":          row.SortAt.Format(time.RFC3339Nano),
			"count":           row.Count,
			"latestRecordUri": row.LatestRecordURI,
			"latestRecordCid": row.LatestRecordCID,
			"latestAuthor":    row.LatestAuthor,
			"isRead":          !row.SortAt.After(watermark),
		}
		edges = append(edges, map[string]interface{}{
			"cursor": cursor,
			"node":   node,
		})
	}

	return map[string]interface{}{
		"edges": edges,
		"pageInfo": map[string]interface{}{
			"hasNextPage":     result.NextCursor != "",
			"hasPreviousPage": after != "",
			"startCursor":     startCursor,
			"endCursor":       endCursor,
		},
	}, nil
}

// UnreadCount is the resolver for `Query.unreadNotificationCount(did)`.
func (r *Resolver) UnreadCount(ctx context.Context, did string) (map[string]interface{}, error) {
	if did == "" {
		return nil, fmt.Errorf("did is required")
	}
	count, more, err := r.repo.UnreadCount(ctx, did)
	if err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"count": count,
		"more":  more,
	}, nil
}

// UpdateSeen is the resolver for `Mutation.updateNotificationsSeen(did, seenAt)`.
func (r *Resolver) UpdateSeen(ctx context.Context, did string, seenAtStr string) (bool, error) {
	if did == "" {
		return false, fmt.Errorf("did is required")
	}
	var seenAt time.Time
	if seenAtStr != "" {
		parsed, err := time.Parse(time.RFC3339Nano, seenAtStr)
		if err != nil {
			parsed, err = time.Parse(time.RFC3339, seenAtStr)
			if err != nil {
				return false, fmt.Errorf("invalid seenAt: %w", err)
			}
		}
		seenAt = parsed
	}
	if err := r.repo.UpdateSeen(ctx, did, seenAt); err != nil {
		return false, err
	}
	return true, nil
}

// nonEmptyOrNil returns nil for empty strings, for GraphQL nullable fields.
func nonEmptyOrNil(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// UnreadCountType is the GraphQL type for the unreadNotificationCount result.
var UnreadCountType = graphql.NewObject(graphql.ObjectConfig{
	Name: "UnreadCountResult",
	Fields: graphql.Fields{
		"count": &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"more":  &graphql.Field{Type: graphql.NewNonNull(graphql.Boolean)},
	},
})

// NotificationType is the GraphQL type for a notification row.
var NotificationType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Notification",
	Fields: graphql.Fields{
		"id":              &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
		"reason":          &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"reasonSubject":   &graphql.Field{Type: graphql.String},
		"sortAt":          &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"count":           &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"latestRecordUri": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"latestRecordCid": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"latestAuthor":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"isRead":          &graphql.Field{Type: graphql.NewNonNull(graphql.Boolean)},
	},
})

// NotificationEdgeType is the GraphQL type for an edge in a notification connection.
var NotificationEdgeType = graphql.NewObject(graphql.ObjectConfig{
	Name: "NotificationEdge",
	Fields: graphql.Fields{
		"cursor": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"node":   &graphql.Field{Type: graphql.NewNonNull(NotificationType)},
	},
})

// NotificationPageInfoType is the GraphQL type for pagination info on notifications.
var NotificationPageInfoType = graphql.NewObject(graphql.ObjectConfig{
	Name: "NotificationPageInfo",
	Fields: graphql.Fields{
		"hasNextPage":     &graphql.Field{Type: graphql.NewNonNull(graphql.Boolean)},
		"hasPreviousPage": &graphql.Field{Type: graphql.NewNonNull(graphql.Boolean)},
		"startCursor":     &graphql.Field{Type: graphql.String},
		"endCursor":       &graphql.Field{Type: graphql.String},
	},
})

// NotificationConnectionType is the GraphQL type for the paginated notifications result.
var NotificationConnectionType = graphql.NewObject(graphql.ObjectConfig{
	Name: "NotificationConnection",
	Fields: graphql.Fields{
		"edges":    &graphql.Field{Type: graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(NotificationEdgeType)))},
		"pageInfo": &graphql.Field{Type: graphql.NewNonNull(NotificationPageInfoType)},
	},
})

// QueryFields returns the GraphQL query fields for notifications, to be merged
// into the admin Query type.
func (r *Resolver) QueryFields() graphql.Fields {
	return graphql.Fields{
		"notifications": &graphql.Field{
			Type:        graphql.NewNonNull(NotificationConnectionType),
			Description: "List notifications for a user (admin only).",
			Args: graphql.FieldConfigArgument{
				"did":     &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				"reasons": &graphql.ArgumentConfig{Type: graphql.NewList(graphql.NewNonNull(graphql.String))},
				"first":   &graphql.ArgumentConfig{Type: graphql.Int, DefaultValue: 50},
				"after":   &graphql.ArgumentConfig{Type: graphql.String},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				did, _ := p.Args["did"].(string)
				first, _ := p.Args["first"].(int)
				after, _ := p.Args["after"].(string)
				var reasons []string
				if raw, ok := p.Args["reasons"].([]interface{}); ok {
					for _, v := range raw {
						if s, ok := v.(string); ok {
							reasons = append(reasons, s)
						}
					}
				}
				return r.ListNotifications(p.Context, did, reasons, first, after)
			},
		},
		"unreadNotificationCount": &graphql.Field{
			Type:        graphql.NewNonNull(UnreadCountType),
			Description: "Count unread notifications for a user, capped at UnreadCountCap.",
			Args: graphql.FieldConfigArgument{
				"did": &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				did, _ := p.Args["did"].(string)
				return r.UnreadCount(p.Context, did)
			},
		},
	}
}

// MutationFields returns the GraphQL mutation fields for notifications, to be
// merged into the admin Mutation type.
func (r *Resolver) MutationFields() graphql.Fields {
	return graphql.Fields{
		"updateNotificationsSeen": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Boolean),
			Description: "Mark notifications as seen up to the given timestamp. Defaults to now.",
			Args: graphql.FieldConfigArgument{
				"did":    &graphql.ArgumentConfig{Type: graphql.NewNonNull(graphql.String)},
				"seenAt": &graphql.ArgumentConfig{Type: graphql.String},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				did, _ := p.Args["did"].(string)
				seenAt, _ := p.Args["seenAt"].(string)
				return r.UpdateSeen(p.Context, did, seenAt)
			},
		},
	}
}

// atoi is a small helper to convert a string to int64.
func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}
