// Package admin provides GraphQL types and resolvers for the admin API.
package admin

import (
	"github.com/graphql-go/graphql"
)

// =============================================================================
// Enums
// =============================================================================

// TimeRangeEnum defines time range options for activity queries.
var TimeRangeEnum = graphql.NewEnum(graphql.EnumConfig{
	Name:        "TimeRange",
	Description: "Time range for activity data bucketing",
	Values: graphql.EnumValueConfigMap{
		"ONE_HOUR": &graphql.EnumValueConfig{
			Value:       "ONE_HOUR",
			Description: "Last hour (5-minute buckets)",
		},
		"THREE_HOURS": &graphql.EnumValueConfig{
			Value:       "THREE_HOURS",
			Description: "Last 3 hours (15-minute buckets)",
		},
		"SIX_HOURS": &graphql.EnumValueConfig{
			Value:       "SIX_HOURS",
			Description: "Last 6 hours (30-minute buckets)",
		},
		"ONE_DAY": &graphql.EnumValueConfig{
			Value:       "ONE_DAY",
			Description: "Last 24 hours (hourly buckets)",
		},
		"SEVEN_DAYS": &graphql.EnumValueConfig{
			Value:       "SEVEN_DAYS",
			Description: "Last 7 days (daily buckets)",
		},
	},
})

// LabelSeverityEnum defines severity levels for labels.
var LabelSeverityEnum = graphql.NewEnum(graphql.EnumConfig{
	Name:        "LabelSeverity",
	Description: "Severity level of a label",
	Values: graphql.EnumValueConfigMap{
		"INFORM": &graphql.EnumValueConfig{
			Value:       "inform",
			Description: "Informational label",
		},
		"ALERT": &graphql.EnumValueConfig{
			Value:       "alert",
			Description: "Content warning label",
		},
		"TAKEDOWN": &graphql.EnumValueConfig{
			Value:       "takedown",
			Description: "Content should be removed",
		},
	},
})

// LabelVisibilityEnum defines visibility options for labeled content.
var LabelVisibilityEnum = graphql.NewEnum(graphql.EnumConfig{
	Name:        "LabelVisibility",
	Description: "How to display labeled content",
	Values: graphql.EnumValueConfigMap{
		"IGNORE": &graphql.EnumValueConfig{
			Value:       "ignore",
			Description: "Ignore the label, show content normally",
		},
		"SHOW": &graphql.EnumValueConfig{
			Value:       "show",
			Description: "Show label indicator but don't hide content",
		},
		"WARN": &graphql.EnumValueConfig{
			Value:       "warn",
			Description: "Show warning before displaying content",
		},
		"HIDE": &graphql.EnumValueConfig{
			Value:       "hide",
			Description: "Hide content (accessible via direct link)",
		},
	},
})

// ReportReasonTypeEnum defines types of reasons for reports.
var ReportReasonTypeEnum = graphql.NewEnum(graphql.EnumConfig{
	Name:        "ReportReasonType",
	Description: "Reason for submitting a moderation report",
	Values: graphql.EnumValueConfigMap{
		"SPAM": &graphql.EnumValueConfig{
			Value:       "spam",
			Description: "Spam or unwanted content",
		},
		"VIOLATION": &graphql.EnumValueConfig{
			Value:       "violation",
			Description: "Terms of service violation",
		},
		"MISLEADING": &graphql.EnumValueConfig{
			Value:       "misleading",
			Description: "Misleading or false information",
		},
		"SEXUAL": &graphql.EnumValueConfig{
			Value:       "sexual",
			Description: "Inappropriate sexual content",
		},
		"RUDE": &graphql.EnumValueConfig{
			Value:       "rude",
			Description: "Harassment or rude behavior",
		},
		"OTHER": &graphql.EnumValueConfig{
			Value:       "other",
			Description: "Other reason (see reason field)",
		},
	},
})

// ReportStatusEnum defines status options for moderation reports.
var ReportStatusEnum = graphql.NewEnum(graphql.EnumConfig{
	Name:        "ReportStatus",
	Description: "Status of a moderation report",
	Values: graphql.EnumValueConfigMap{
		"PENDING": &graphql.EnumValueConfig{
			Value:       "pending",
			Description: "Awaiting review",
		},
		"RESOLVED": &graphql.EnumValueConfig{
			Value:       "resolved",
			Description: "Action taken",
		},
		"DISMISSED": &graphql.EnumValueConfig{
			Value:       "dismissed",
			Description: "No action needed",
		},
	},
})

// ReportActionEnum defines actions for resolving reports.
var ReportActionEnum = graphql.NewEnum(graphql.EnumConfig{
	Name:        "ReportAction",
	Description: "Action to take when resolving a report",
	Values: graphql.EnumValueConfigMap{
		"APPLY_LABEL": &graphql.EnumValueConfig{
			Value:       "apply_label",
			Description: "Apply a label to the content",
		},
		"DISMISS": &graphql.EnumValueConfig{
			Value:       "dismiss",
			Description: "Dismiss the report without action",
		},
	},
})

// =============================================================================
// Object Types
// =============================================================================

// StatisticsType represents system statistics.
var StatisticsType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "Statistics",
	Description: "System statistics",
	Fields: graphql.Fields{
		"recordCount": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total number of records",
		},
		"actorCount": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total number of actors",
		},
		"lexiconCount": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total number of lexicons",
		},
	},
})

// CurrentSessionType represents the current user session.
var CurrentSessionType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "CurrentSession",
	Description: "Current authenticated user session",
	Fields: graphql.Fields{
		"did": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "User's DID",
		},
		"handle": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "User's handle",
		},
		"isAdmin": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Boolean),
			Description: "Whether the user has admin privileges",
		},
	},
})

// SettingsType represents system settings.
var SettingsType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "Settings",
	Description: "System settings",
	Fields: graphql.Fields{
		"id": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Global ID for client cache normalization",
		},
		"domainAuthority": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Domain authority (e.g., example.com)",
		},
		"adminDids": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(graphql.String))),
			Description: "List of admin DIDs",
		},
		"relayUrl": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "AT Protocol relay URL for backfill",
		},
		"plcDirectoryUrl": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "PLC directory URL for DID resolution",
		},
		"jetstreamUrl": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Jetstream WebSocket endpoint",
		},
		"oauthSupportedScopes": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Space-separated list of supported OAuth scopes",
		},
	},
})

// ActivityBucketType represents aggregated activity for a time bucket.
var ActivityBucketType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "ActivityBucket",
	Description: "Aggregated activity data for a time bucket",
	Fields: graphql.Fields{
		"timestamp": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Bucket timestamp (ISO 8601)",
		},
		"total": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total operations in bucket",
		},
		"creates": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Create operations",
		},
		"updates": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Update operations",
		},
		"deletes": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Delete operations",
		},
	},
})

// ActivityEntryType represents a single activity log entry.
var ActivityEntryType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "ActivityEntry",
	Description: "A single jetstream activity log entry",
	Fields: graphql.Fields{
		"id": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Entry ID",
		},
		"timestamp": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Timestamp (ISO 8601)",
		},
		"operation": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Operation type (create, update, delete)",
		},
		"collection": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Collection NSID",
		},
		"did": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Actor DID",
		},
		"rkey": &graphql.Field{
			Type:        graphql.String,
			Description: "Record key",
		},
		"status": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Processing status",
		},
		"errorMessage": &graphql.Field{
			Type:        graphql.String,
			Description: "Error message if processing failed",
		},
		"eventJson": &graphql.Field{
			Type:        graphql.String,
			Description: "Raw event JSON",
		},
		"isValid": &graphql.Field{
			Type:        graphql.Boolean,
			Description: "Whether the record passed lexicon validation (null if validation not run)",
		},
	},
})

// ValidationStatsType represents aggregated validation statistics.
var ValidationStatsType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "ValidationStats",
	Description: "Aggregated validation statistics",
	Fields: graphql.Fields{
		"invalidCount": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total number of invalid records in the time range",
		},
		"invalidByCollection": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(CollectionValidationCountType))),
			Description: "Invalid record counts grouped by collection",
		},
		"recentInvalid": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(ActivityEntryType))),
			Description: "Most recent invalid activity entries",
		},
		"lastInvalidAt": &graphql.Field{
			Type:        graphql.String,
			Description: "Timestamp of the most recent invalid record (ISO 8601)",
		},
	},
})

// CollectionValidationCountType represents a per-collection invalid record count.
var CollectionValidationCountType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "CollectionValidationCount",
	Description: "Invalid record count for a specific collection",
	Fields: graphql.Fields{
		"collection": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Collection NSID",
		},
		"count": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Number of invalid records",
		},
	},
})

// PurgeActorPreviewType is what previewPurgeActor returns to the
// operator. Contains the counts they confirm against plus the
// HMAC-signed `confirmToken` they hand back to purgeActor. See
// internal/graphql/admin/purge.go.
var PurgeActorPreviewType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "PurgeActorPreview",
	Description: "Preview of what an actor purge would delete, plus a single-use confirmation token",
	Fields: graphql.Fields{
		"did": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "The DID that would be purged",
		},
		"recordCount": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Number of records that would be deleted",
		},
		"actorExists": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Boolean),
			Description: "Whether the indexer has an actor row for this DID",
		},
		"handle": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Actor handle if known (empty when actorExists=false)",
		},
		"latestIndexedAt": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Actor's last-indexed-at timestamp in RFC3339 (empty when actorExists=false)",
		},
		"confirmToken": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "HMAC-signed token bound to (admin_did, target_did, record_count). Pass back to purgeActor before tokenExpiresAt",
		},
		"tokenExpiresAt": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "RFC3339 timestamp at which confirmToken stops being accepted",
		},
		"tokenTtlSeconds": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Seconds the token is valid for, for client-side countdown display",
		},
	},
})

// TableCountType is a per-table row count returned by
// previewResetAll. The list is sized by the resetAll deletion list
// (see internal/graphql/admin/resolvers.go); name is the literal
// Postgres table identifier.
var TableCountType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "TableCount",
	Description: "Per-table row count in a reset-all preview",
	Fields: graphql.Fields{
		"name": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Postgres table identifier",
		},
		"count": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Current row count in the table",
		},
	},
})

// ResetAllPreviewType is what previewResetAll returns to the
// operator. Mirrors the actor-purge preview contract: counts the
// operator confirms against plus an HMAC-signed token they hand
// back to resetAll. The token is scope-bound to reset_all so it
// cannot be redeemed against any other destructive admin
// mutation.
var ResetAllPreviewType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "ResetAllPreview",
	Description: "Preview of what resetAll would delete, plus a single-use confirmation token bound to (admin DID, total row count, expiry, scope=reset_all)",
	Fields: graphql.Fields{
		"totalRows": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Sum of row counts across every table in the deletion list",
		},
		"tables": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(TableCountType))),
			Description: "Per-table breakdown (matches the deletion-list order in the resolver)",
		},
		"confirmToken": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "HMAC-signed token bound to (admin_did, total_rows, exp, scope=reset_all). Pass back to resetAll before tokenExpiresAt",
		},
		"tokenExpiresAt": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "RFC3339 timestamp at which confirmToken stops being accepted",
		},
		"tokenTtlSeconds": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Seconds the token is valid for, for client-side countdown display",
		},
	},
})

// ResetAllResultType is what resetAll returns on success.
var ResetAllResultType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "ResetAllResult",
	Description: "Result of a reset-all operation",
	Fields: graphql.Fields{
		"rowsDeleted": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total rows deleted across every table in the deletion list",
		},
		"tablesAffected": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Number of tables touched (i.e. the size of the deletion list)",
		},
	},
})

// PurgeActorResultType is what purgeActor returns on success.
var PurgeActorResultType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "PurgeActorResult",
	Description: "Result of an actor purge operation",
	Fields: graphql.Fields{
		"did": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "The DID that was purged",
		},
		"recordsDeleted": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Number of records actually deleted in the SQL transaction",
		},
		"actorRowsDeleted": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "1 if the actor row was present and deleted, 0 otherwise",
		},
		"tapStatus": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Post-commit Tap removal status: removed | failed | skipped",
		},
	},
})

// CollectionOverviewType represents per-collection record and validation counts.
var CollectionOverviewType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "CollectionOverview",
	Description: "Per-collection record count and validation status",
	Fields: graphql.Fields{
		"collection":   &graphql.Field{Type: graphql.NewNonNull(graphql.String), Description: "Collection NSID"},
		"recordCount":  &graphql.Field{Type: graphql.NewNonNull(graphql.Int), Description: "Total number of records"},
		"invalidCount": &graphql.Field{Type: graphql.NewNonNull(graphql.Int), Description: "Number of invalid records"},
	},
})

// LexiconType represents a lexicon schema definition.
var LexiconType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "Lexicon",
	Description: "A lexicon schema definition",
	Fields: graphql.Fields{
		"id": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Lexicon NSID (e.g., app.bsky.feed.post)",
		},
		"json": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Full lexicon JSON content",
		},
		"createdAt": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Timestamp when lexicon was created",
		},
	},
})

// OAuthClientType represents an OAuth client registration.
var OAuthClientType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "OAuthClient",
	Description: "An OAuth client registration",
	Fields: graphql.Fields{
		"clientId": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Client ID",
		},
		"clientSecret": &graphql.Field{
			Type:        graphql.String,
			Description: "Client secret (only for confidential clients)",
		},
		"clientName": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Client display name",
		},
		"clientType": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Client type (PUBLIC or CONFIDENTIAL)",
		},
		"redirectUris": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(graphql.String))),
			Description: "Allowed redirect URIs",
		},
		"scope": &graphql.Field{
			Type:        graphql.String,
			Description: "Allowed OAuth scopes (space-separated)",
		},
		"createdAt": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Creation timestamp (Unix epoch)",
		},
	},
})

// LabelDefinitionType represents a label type definition. Post-issue-#2,
// definitions are scoped to a specific labeler via `src`.
var LabelDefinitionType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "LabelDefinition",
	Description: "A label type definition owned by a specific labeler",
	Fields: graphql.Fields{
		"src": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "DID of the labeler that owns this definition (did:web:system for pre-seeded defaults)",
		},
		"val": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Label value (e.g., 'porn', '!takedown')",
		},
		"description": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Human-readable description",
		},
		"severity": &graphql.Field{
			Type:        graphql.NewNonNull(LabelSeverityEnum),
			Description: "Severity level",
		},
		"defaultVisibility": &graphql.Field{
			Type:        graphql.NewNonNull(LabelVisibilityEnum),
			Description: "Default visibility setting",
		},
		"createdAt": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Creation timestamp (ISO 8601)",
		},
	},
})

// LabelPreferenceType represents a user's preference for a specific
// (labeler, label value) combination.
var LabelPreferenceType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "LabelPreference",
	Description: "A user's preference for a specific (labeler, label value) combination",
	Fields: graphql.Fields{
		"src": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "DID of the labeler this preference applies to",
		},
		"val": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Label value",
		},
		"description": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Label description",
		},
		"severity": &graphql.Field{
			Type:        graphql.NewNonNull(LabelSeverityEnum),
			Description: "Label severity",
		},
		"defaultVisibility": &graphql.Field{
			Type:        graphql.NewNonNull(LabelVisibilityEnum),
			Description: "Default visibility setting",
		},
		"visibility": &graphql.Field{
			Type:        graphql.NewNonNull(LabelVisibilityEnum),
			Description: "User's effective visibility setting",
		},
	},
})

// LabelType represents an applied label.
var LabelType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "Label",
	Description: "An applied label on a record or account",
	Fields: graphql.Fields{
		"id": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Label ID",
		},
		"src": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "DID of the labeler who applied this",
		},
		"uri": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Subject URI (at:// or did:)",
		},
		"cid": &graphql.Field{
			Type:        graphql.String,
			Description: "Optional CID for version-specific label",
		},
		"val": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Label value",
		},
		"neg": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Boolean),
			Description: "True if this is a negation (retraction)",
		},
		"cts": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Creation timestamp (ISO 8601)",
		},
		"exp": &graphql.Field{
			Type:        graphql.String,
			Description: "Optional expiration timestamp",
		},
	},
})

// ReportType represents a moderation report.
var ReportType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "Report",
	Description: "A user-submitted moderation report",
	Fields: graphql.Fields{
		"id": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Report ID",
		},
		"reporterDid": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "DID of the reporter",
		},
		"subjectUri": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Subject URI (at:// or did:)",
		},
		"reasonType": &graphql.Field{
			Type:        graphql.NewNonNull(ReportReasonTypeEnum),
			Description: "Reason type",
		},
		"reason": &graphql.Field{
			Type:        graphql.String,
			Description: "Optional free-text explanation",
		},
		"status": &graphql.Field{
			Type:        graphql.NewNonNull(ReportStatusEnum),
			Description: "Report status",
		},
		"resolvedBy": &graphql.Field{
			Type:        graphql.String,
			Description: "DID of admin who resolved (if resolved)",
		},
		"resolvedAt": &graphql.Field{
			Type:        graphql.String,
			Description: "Resolution timestamp (if resolved)",
		},
		"createdAt": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Creation timestamp (ISO 8601)",
		},
	},
})

// =============================================================================
// Connection Types (Relay-style pagination)
// =============================================================================

// PageInfoType represents pagination info.
var PageInfoType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "PageInfo",
	Description: "Information about pagination in a connection",
	Fields: graphql.Fields{
		"hasNextPage": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Boolean),
			Description: "Whether more items exist after this page",
		},
		"hasPreviousPage": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Boolean),
			Description: "Whether more items exist before this page",
		},
		"startCursor": &graphql.Field{
			Type:        graphql.String,
			Description: "Cursor of the first item",
		},
		"endCursor": &graphql.Field{
			Type:        graphql.String,
			Description: "Cursor of the last item",
		},
	},
})

// LabelEdgeType represents an edge in the label connection.
var LabelEdgeType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "LabelEdge",
	Description: "An edge in the label connection",
	Fields: graphql.Fields{
		"cursor": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Cursor for this item",
		},
		"node": &graphql.Field{
			Type:        graphql.NewNonNull(LabelType),
			Description: "The label",
		},
	},
})

// LabelConnectionType represents a paginated list of labels.
var LabelConnectionType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "LabelConnection",
	Description: "A paginated list of labels",
	Fields: graphql.Fields{
		"edges": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(LabelEdgeType))),
			Description: "List of label edges",
		},
		"pageInfo": &graphql.Field{
			Type:        graphql.NewNonNull(PageInfoType),
			Description: "Pagination info",
		},
		"totalCount": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total number of labels matching the filter",
		},
	},
})

// ReportEdgeType represents an edge in the report connection.
var ReportEdgeType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "ReportEdge",
	Description: "An edge in the report connection",
	Fields: graphql.Fields{
		"cursor": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Cursor for this item",
		},
		"node": &graphql.Field{
			Type:        graphql.NewNonNull(ReportType),
			Description: "The report",
		},
	},
})

// ReportConnectionType represents a paginated list of reports.
var ReportConnectionType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "ReportConnection",
	Description: "A paginated list of reports",
	Fields: graphql.Fields{
		"edges": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(ReportEdgeType))),
			Description: "List of report edges",
		},
		"pageInfo": &graphql.Field{
			Type:        graphql.NewNonNull(PageInfoType),
			Description: "Pagination info",
		},
		"totalCount": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total number of reports matching the filter",
		},
	},
})
