// Package schema provides the GraphQL schema builder.
package schema

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/database/repositories"
	"github.com/GainForest/hypergoat/internal/graphql/query"
	"github.com/GainForest/hypergoat/internal/graphql/resolver"
	"github.com/GainForest/hypergoat/internal/graphql/subscription"
	"github.com/GainForest/hypergoat/internal/graphql/types"
	"github.com/GainForest/hypergoat/internal/lexicon"
	"github.com/GainForest/hypergoat/internal/metrics"
)

// Builder builds a GraphQL schema from lexicon definitions.
type Builder struct {
	registry      *lexicon.Registry
	mapper        *types.Mapper
	objectBuilder *types.ObjectBuilder

	// Built types
	recordTypes     map[string]*graphql.Object // lexiconID -> record type
	connectionTypes map[string]*graphql.Object // lexiconID -> connection type
}

// NewBuilder creates a new schema builder.
func NewBuilder(registry *lexicon.Registry) *Builder {
	mapper := types.NewMapper()
	return &Builder{
		registry:        registry,
		mapper:          mapper,
		objectBuilder:   types.NewObjectBuilder(mapper, registry),
		recordTypes:     make(map[string]*graphql.Object),
		connectionTypes: make(map[string]*graphql.Object),
	}
}

// Build builds the complete GraphQL schema.
func (b *Builder) Build() (*graphql.Schema, error) {
	// Phase 1: Build all object types (non-record helper types)
	b.buildObjectTypes()

	// Phase 2: Build all record types
	b.buildRecordTypes()

	// Phase 3: Build connection types
	b.buildConnectionTypes()

	// Phase 4: Build Query type
	queryType := b.buildQueryType()

	// Phase 5: Build Subscription type
	subscriptionType := b.buildSubscriptionType()

	// Create schema
	schema, err := graphql.NewSchema(graphql.SchemaConfig{
		Query:        queryType,
		Subscription: subscriptionType,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create schema: %w", err)
	}

	return &schema, nil
}

// buildObjectTypes builds GraphQL types for all non-record definitions.
func (b *Builder) buildObjectTypes() {
	// Get all lexicons that only have defs (no main record)
	for _, lex := range b.registry.GetDefsLexicons() {
		for defName, def := range lex.Defs.Others {
			if def.IsObject() {
				ref := lexicon.MakeRef(lex.ID, defName)
				b.objectBuilder.BuildObjectType(ref, def.Object)
			}
		}
	}

	// Also build object defs from collection lexicons
	for _, lex := range b.registry.GetCollectionLexicons() {
		for defName, def := range lex.Defs.Others {
			if def.IsObject() {
				ref := lexicon.MakeRef(lex.ID, defName)
				b.objectBuilder.BuildObjectType(ref, def.Object)
			}
		}
	}
}

// buildRecordTypes builds GraphQL types for all record definitions.
func (b *Builder) buildRecordTypes() {
	for _, lex := range b.registry.GetCollectionLexicons() {
		if lex.Defs.Main != nil {
			recordType := b.objectBuilder.BuildRecordType(lex.ID, lex.Defs.Main)
			b.recordTypes[lex.ID] = recordType
		}
	}
}

// buildConnectionTypes builds Relay connection types for all record types.
func (b *Builder) buildConnectionTypes() {
	for lexiconID, recordType := range b.recordTypes {
		connectionType := query.BuildConnectionType(recordType)
		b.connectionTypes[lexiconID] = connectionType
	}
}

// RecordEvent GraphQL type for subscriptions.
//
// The `labels` field is populated via a per-event resolver that
// batch-loads from the labels repo — the subscription handler itself
// does not enrich events with labels because it's in a separate
// package and has no direct access to repositories.
var recordEventType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "RecordEvent",
	Description: "A real-time record change event",
	Fields: graphql.Fields{
		"type": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Event type: create, update, or delete",
		},
		"uri": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "AT-URI of the record",
		},
		"cid": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "CID of the record",
		},
		"did": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "DID of the actor who made the change",
		},
		"collection": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Collection NSID",
		},
		"record": &graphql.Field{
			Type:        types.JSONScalar,
			Description: "The record data (null for delete events)",
		},
		"labels": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(graphql.String))),
			Description: "Active label values on this record from any ingested labeler. Always a list (possibly empty), never null.",
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				src, ok := p.Source.(map[string]interface{})
				if !ok {
					return []string{}, nil
				}
				uri, _ := src["uri"].(string)
				if uri == "" {
					return []string{}, nil
				}
				repos := resolver.GetRepositories(p.Context)
				if repos == nil {
					return []string{}, nil
				}
				rec := &repositories.Record{URI: uri}
				labelsByURI := loadLabelsByURI(p.Context, repos, nil, []*repositories.Record{rec})
				return labelsByURI[uri], nil
			},
		},
	},
})

// CollectionStat GraphQL type for collection statistics
var collectionStatType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "CollectionStat",
	Description: "Statistics for a collection",
	Fields: graphql.Fields{
		"collection": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Collection NSID",
		},
		"count": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Number of records in the collection",
		},
	},
})

// TimeSeriesPoint GraphQL type for time series data points
var timeSeriesPointType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "TimeSeriesPoint",
	Description: "A single data point in a time series",
	Fields: graphql.Fields{
		"date": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Date in YYYY-MM-DD format",
		},
		"count": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Number of records on this date",
		},
		"cumulative": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Cumulative count up to and including this date",
		},
	},
})

// CollectionTimeSeries GraphQL type for collection time series data
var collectionTimeSeriesType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "CollectionTimeSeries",
	Description: "Time series data for a collection",
	Fields: graphql.Fields{
		"collection": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Collection NSID",
		},
		"totalRecords": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Total number of records in the collection",
		},
		"uniqueUsers": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.Int),
			Description: "Number of unique users (DIDs) in the collection",
		},
		"data": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(timeSeriesPointType))),
			Description: "Time series data points",
		},
	},
})

// buildSubscriptionType builds the root Subscription type.
func (b *Builder) buildSubscriptionType() *graphql.Object {
	fields := graphql.Fields{
		// Subscribe to all record events
		"recordEvents": &graphql.Field{
			Type:        recordEventType,
			Description: "Subscribe to all record change events",
			Args: graphql.FieldConfigArgument{
				"collection": &graphql.ArgumentConfig{
					Type:        graphql.String,
					Description: "Filter by collection NSID (optional)",
				},
			},
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				// Extract recordEvents from the root object passed by subscription handler
				if m, ok := p.Source.(map[string]interface{}); ok {
					return m["recordEvents"], nil
				}
				return p.Source, nil
			},
		},
	}

	// Add per-collection subscriptions. Each resolver injects the
	// record's active labels into the emitted payload so subscribers
	// see the same `labels` field that queries surface — otherwise a
	// subscription client would always see null for labels.
	for lexiconID, recordType := range b.recordTypes {
		fieldName := lexicon.ToFieldName(lexiconID) + "Events"
		collection := lexiconID // Capture for closure

		fields[fieldName] = &graphql.Field{
			Type:        recordType,
			Description: fmt.Sprintf("Subscribe to %s record changes", lexiconID),
			Resolve: func(p graphql.ResolveParams) (interface{}, error) {
				event, ok := p.Source.(*subscription.RecordEvent)
				if !ok || event == nil {
					return nil, nil
				}
				// Only return if collection matches
				if event.Collection != collection {
					return nil, nil
				}
				// Clone the record map to avoid concurrent map writes
				// when multiple subscribers receive the same event.
				var record map[string]interface{}
				if event.Record != nil {
					record = make(map[string]interface{}, len(event.Record)+1)
					for k, v := range event.Record {
						record[k] = v
					}
				}

				// Best-effort label attachment. We synthesize a minimal
				// Record so loadLabelsByURI can batch-load; the helper
				// tolerates DB failures and returns empty slices on
				// error so subscription delivery is never blocked.
				repos := resolver.GetRepositories(p.Context)
				if repos != nil && record != nil {
					rec := &repositories.Record{URI: event.URI}
					labelsByURI := loadLabelsByURI(p.Context, repos, nil, []*repositories.Record{rec})
					record["labels"] = labelsByURI[event.URI]
				}
				return record, nil
			},
		}
	}

	return graphql.NewObject(graphql.ObjectConfig{
		Name:   "Subscription",
		Fields: fields,
	})
}

// Generic record type for the records query
var genericRecordType = graphql.NewObject(graphql.ObjectConfig{
	Name:        "GenericRecord",
	Description: "A generic AT Protocol record",
	Fields: graphql.Fields{
		"uri": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "AT-URI of the record",
		},
		"cid": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "CID of the record",
		},
		"did": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "DID of the actor",
		},
		"collection": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Collection NSID",
		},
		"rkey": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Record key",
		},
		"value": &graphql.Field{
			Type:        types.JSONScalar,
			Description: "The record data as JSON",
		},
		"labels": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(graphql.String))),
			Description: "Active label values on this record from any ingested labeler. Always a list (possibly empty), never null.",
		},
	},
})

// Generic record edge for pagination
var genericRecordEdgeType = graphql.NewObject(graphql.ObjectConfig{
	Name: "GenericRecordEdge",
	Fields: graphql.Fields{
		"cursor": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"node":   &graphql.Field{Type: genericRecordType},
	},
})

// Generic record connection for pagination
var genericRecordConnectionType = graphql.NewObject(graphql.ObjectConfig{
	Name: "GenericRecordConnection",
	Fields: graphql.Fields{
		"edges":    &graphql.Field{Type: graphql.NewList(genericRecordEdgeType)},
		"pageInfo": &graphql.Field{Type: query.PageInfoType},
	},
})

// buildQueryType builds the root Query type with fields for each collection.
func (b *Builder) buildQueryType() *graphql.Object {
	fields := graphql.Fields{}

	// Add generic records query that works for any collection. The label
	// filter args mirror the per-lexicon collection queries so the
	// `records` endpoint supports the same feeds without client-side
	// merging.
	genericRecordsArgs := graphql.FieldConfigArgument{
		"collection": &graphql.ArgumentConfig{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Collection NSID (e.g., org.impactindexer.review.like)",
		},
		"first": &graphql.ArgumentConfig{
			Type:         graphql.Int,
			DefaultValue: 20,
			Description:  "Number of records to return",
		},
		"after": &graphql.ArgumentConfig{
			Type:        graphql.String,
			Description: "Cursor for pagination",
		},
	}
	for k, v := range query.LabelFilterArgs() {
		genericRecordsArgs[k] = v
	}
	fields["records"] = &graphql.Field{
		Type:        genericRecordConnectionType,
		Description: "Query records from any collection (useful for collections without lexicon schemas)",
		Args:        genericRecordsArgs,
		Resolve:     b.createGenericRecordsResolver(),
	}

	// Add collectionStats query for efficient aggregate counts
	fields["collectionStats"] = &graphql.Field{
		Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(collectionStatType))),
		Description: "Get record counts for collections (efficient aggregate query)",
		Args: graphql.FieldConfigArgument{
			"collections": &graphql.ArgumentConfig{
				Type:        graphql.NewList(graphql.NewNonNull(graphql.String)),
				Description: "Filter by collection NSIDs (optional, returns all if not specified)",
			},
		},
		Resolve: b.createCollectionStatsResolver(),
	}

	// Add collectionTimeSeries query for time series data
	fields["collectionTimeSeries"] = &graphql.Field{
		Type:        collectionTimeSeriesType,
		Description: "Get time series data for a collection (records grouped by date)",
		Args: graphql.FieldConfigArgument{
			"collection": &graphql.ArgumentConfig{
				Type:        graphql.NewNonNull(graphql.String),
				Description: "Collection NSID",
			},
		},
		Resolve: b.createCollectionTimeSeriesResolver(),
	}

	for lexiconID, connectionType := range b.connectionTypes {
		fieldName := lexicon.ToFieldName(lexiconID)

		fields[fieldName] = &graphql.Field{
			Type:        connectionType,
			Description: fmt.Sprintf("Query %s records", lexiconID),
			Args:        query.ConnectionArgs(),
			Resolve:     b.createCollectionResolver(lexiconID),
		}

		// Also add a singular lookup by URI
		recordType := b.recordTypes[lexiconID]
		fields[fieldName+"ByUri"] = &graphql.Field{
			Type:        recordType,
			Description: fmt.Sprintf("Get a single %s by AT-URI", lexiconID),
			Args: graphql.FieldConfigArgument{
				"uri": &graphql.ArgumentConfig{
					Type:        graphql.NewNonNull(graphql.String),
					Description: "AT-URI of the record",
				},
			},
			Resolve: b.createSingleRecordResolver(lexiconID),
		}
	}

	return graphql.NewObject(graphql.ObjectConfig{
		Name:   "Query",
		Fields: fields,
	})
}

// nodeBuilder transforms a Record and its parsed JSON into a GraphQL node.
type nodeBuilder func(rec *repositories.Record, value map[string]interface{}) (interface{}, bool)

// resolveRecordConnection is the shared implementation for paginated record queries.
// It uses deterministic keyset pagination with a composite (indexed_at, uri) cursor,
// and applies optional label-based filtering when the caller supplies `labels` or
// `excludeLabels` arguments.
func (b *Builder) resolveRecordConnection(
	p graphql.ResolveParams,
	collection string,
	buildNode nodeBuilder,
) (interface{}, error) {
	repos := resolver.GetRepositories(p.Context)
	if repos == nil || repos.Records == nil {
		return emptyConnection(), nil
	}

	// Extract pagination args. ClampPageSize caps `first` at
	// MaxPageSize (100) and defaults to DefaultPageSize (20) when
	// unset or non-positive, so a client can't ask for a million
	// records in one request.
	rawFirst, _ := p.Args["first"].(int)
	first := query.ClampPageSize(rawFirst)
	after, _ := p.Args["after"].(string)

	// Decode composite cursor if provided
	var afterTimestamp, afterURI string
	if after != "" {
		var err error
		afterTimestamp, afterURI, err = decodeCursor(after)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
	}

	// Label filter args. The indexer is neutral about labeler choice:
	// an empty LabelerSrcs list means "match any labeler".
	labelFilter := parseLabelFilter(p.Args)

	// Author filter args. A nil return means "no filter" (omitted);
	// a non-nil empty slice means "match nothing" (explicit empty list).
	authorsFilter, err := query.ParseAuthorsFilter(p.Args)
	if err != nil {
		if errors.Is(err, repositories.ErrAuthorsFilterTooLarge) ||
			strings.Contains(err.Error(), "exceeds maximum") {
			metrics.RecordAuthorsFilterTooLarge()
		}
		return nil, fmt.Errorf("invalid authors argument: %w", err)
	}

	filter := repositories.RecordFilter{Labels: labelFilter}
	if authorsFilter != nil {
		filter.Authors = *authorsFilter
		if len(*authorsFilter) == 0 {
			metrics.RecordAuthorsFilterEmptyBlocked()
		} else {
			metrics.RecordAuthorsFilterApplied(collection, len(*authorsFilter))
		}
	}

	// Fetch first+1 to determine hasNextPage
	records, err := repos.Records.GetByCollectionFiltered(
		p.Context, collection, first+1, afterTimestamp, afterURI, filter,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query records: %w", err)
	}

	// Determine if there are more results
	hasNextPage := len(records) > first
	if hasNextPage {
		records = records[:first]
	}

	// Batch-load active labels for all records on this page so we can
	// attach a `labels` field to each node in one query instead of N.
	// When the filter scopes labelers, we honor that in the returned
	// labels field too so the list the client sees matches the filter.
	labelsByURI := loadLabelsByURI(p.Context, repos, labelFilter.LabelerSrcs, records)

	// Build edges
	edges := make([]interface{}, 0, len(records))
	var startCursor, endCursor string

	for _, rec := range records {
		var value map[string]interface{}
		if err := json.Unmarshal([]byte(rec.JSON), &value); err != nil {
			continue // Skip records with invalid JSON
		}

		node, ok := buildNode(rec, value)
		if !ok {
			continue
		}

		// Attach the labels list. `labels` is a reserved record field
		// (see types.ReservedRecordFields) so any lexicon property
		// with the same name is dropped at schema build time, which
		// means we can unconditionally overwrite whatever might be
		// in the record's JSON payload here.
		if nodeMap, ok := node.(map[string]interface{}); ok {
			nodeMap["labels"] = labelsByURI[rec.URI]
		}

		cursor := encodeCursor(rec.IndexedAt.Format("2006-01-02T15:04:05Z"), rec.URI)
		if startCursor == "" {
			startCursor = cursor
		}
		endCursor = cursor

		edges = append(edges, map[string]interface{}{
			"cursor": cursor,
			"node":   node,
		})
	}

	return map[string]interface{}{
		"edges": edges,
		"pageInfo": map[string]interface{}{
			"hasNextPage":     hasNextPage,
			"hasPreviousPage": after != "",
			"startCursor":     startCursor,
			"endCursor":       endCursor,
		},
	}, nil
}

// MaxLabelFilterValues bounds the number of label values that can be
// passed in a single `labels` or `excludeLabels` GraphQL argument. This
// protects the DB from unbounded `IN (?, ?, ...)` clauses and hitting
// SQLite's 999-parameter limit.
const MaxLabelFilterValues = 50

// MaxLabelFilterLabelers bounds the number of labeler DIDs that can be
// passed in a single `labelerDids` GraphQL argument. Even the largest
// trust sets are nowhere near this in practice.
const MaxLabelFilterLabelers = 32

// parseLabelFilter extracts the label-related arguments from a GraphQL
// field resolver's args map and returns a LabelFilter. The indexer does
// NOT impose a default labeler: when labelerDids is empty, the filter
// matches labels from every labeler that has been ingested. Callers
// restrict to a trust set via the labelerDids arg.
//
// Too-long lists are truncated (with a warn log) to protect the DB.
func parseLabelFilter(args map[string]interface{}) repositories.LabelFilter {
	var filter repositories.LabelFilter

	if raw, ok := args["labelerDids"].([]interface{}); ok {
		for _, v := range raw {
			if len(filter.LabelerSrcs) >= MaxLabelFilterLabelers {
				break
			}
			if s, ok := v.(string); ok && s != "" {
				filter.LabelerSrcs = append(filter.LabelerSrcs, s)
			}
		}
		if len(raw) > MaxLabelFilterLabelers {
			slog.Warn("GraphQL: labelerDids argument truncated",
				"supplied", len(raw), "max", MaxLabelFilterLabelers)
		}
	}

	rawIncludeLen := 0
	if raw, ok := args["labels"].([]interface{}); ok {
		rawIncludeLen = len(raw)
		for _, v := range raw {
			if len(filter.Include) >= MaxLabelFilterValues {
				break
			}
			if s, ok := v.(string); ok && s != "" {
				filter.Include = append(filter.Include, s)
			}
		}
	}
	rawExcludeLen := 0
	if raw, ok := args["excludeLabels"].([]interface{}); ok {
		rawExcludeLen = len(raw)
		for _, v := range raw {
			if len(filter.Exclude) >= MaxLabelFilterValues {
				break
			}
			if s, ok := v.(string); ok && s != "" {
				filter.Exclude = append(filter.Exclude, s)
			}
		}
	}

	if rawIncludeLen > MaxLabelFilterValues {
		slog.Warn("GraphQL: labels argument truncated",
			"supplied", rawIncludeLen, "max", MaxLabelFilterValues)
	}
	if rawExcludeLen > MaxLabelFilterValues {
		slog.Warn("GraphQL: excludeLabels argument truncated",
			"supplied", rawExcludeLen, "max", MaxLabelFilterValues)
	}

	return filter
}

// loadLabelsByURI batch-loads active labels for a page of records and
// groups them into a map of URI -> []val. Every URI in the input is
// present in the result (with an empty slice if no labels match) so the
// GraphQL `labels` field renders as `[]` instead of `null`.
//
// When labelerSrcs is non-empty, only labels from those labelers are
// included. When it is nil/empty, the result is the union of active
// labels from every labeler ingested — the indexer is neutral about
// which labeler to trust, so it surfaces all of them and lets the
// client narrow via the `labelerDids` GraphQL arg.
//
// A DB failure is non-fatal and yields empty slices so the main query
// still succeeds, but the error is logged so operators can detect
// silent label disappearance.
func loadLabelsByURI(
	ctx context.Context,
	repos *resolver.Repositories,
	labelerSrcs []string,
	records []*repositories.Record,
) map[string][]string {
	result := make(map[string][]string, len(records))
	// Prime every record URI with an empty slice so the caller always
	// gets a non-nil []string per record.
	for _, rec := range records {
		result[rec.URI] = []string{}
	}
	if repos.Labels == nil || len(records) == 0 {
		return result
	}

	uris := make([]string, 0, len(records))
	for _, rec := range records {
		uris = append(uris, rec.URI)
	}

	labels, err := repos.Labels.GetByURIs(ctx, uris)
	if err != nil {
		slog.Warn("GraphQL: failed to batch-load labels for page",
			"uri_count", len(uris),
			"labeler_srcs", labelerSrcs,
			"error", err)
		return result
	}

	// Optional trust-set filter: empty means "any labeler".
	var allowed map[string]struct{}
	if len(labelerSrcs) > 0 {
		allowed = make(map[string]struct{}, len(labelerSrcs))
		for _, s := range labelerSrcs {
			allowed[s] = struct{}{}
		}
	}

	for _, l := range labels {
		if allowed != nil {
			if _, ok := allowed[l.Src]; !ok {
				continue
			}
		}
		result[l.URI] = append(result[l.URI], l.Val)
	}
	return result
}

// createGenericRecordsResolver creates a resolver for the generic records query.
func (b *Builder) createGenericRecordsResolver() graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		collection, ok := p.Args["collection"].(string)
		if !ok || collection == "" {
			return nil, fmt.Errorf("collection is required")
		}

		return b.resolveRecordConnection(p, collection,
			func(rec *repositories.Record, value map[string]interface{}) (interface{}, bool) {
				return map[string]interface{}{
					"uri":        rec.URI,
					"cid":        rec.CID,
					"did":        rec.DID,
					"collection": rec.Collection,
					"rkey":       rec.RKey,
					"value":      value,
				}, true
			})
	}
}

// createCollectionResolver creates a resolver for querying a typed collection.
func (b *Builder) createCollectionResolver(lexiconID string) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		return b.resolveRecordConnection(p, lexiconID,
			func(rec *repositories.Record, data map[string]interface{}) (interface{}, bool) {
				// Reserved record fields — always our metadata,
				// always authoritative. Any lexicon property with
				// the same name has already been dropped in
				// buildRecordFields.
				data["uri"] = rec.URI
				data["cid"] = rec.CID
				data["did"] = rec.DID
				data["rkey"] = rec.RKey
				return data, true
			})
	}
}

// createSingleRecordResolver creates a resolver for fetching a single record.
func (b *Builder) createSingleRecordResolver(lexiconID string) graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		uri, ok := p.Args["uri"].(string)
		if !ok {
			return nil, fmt.Errorf("uri is required")
		}

		// Get repositories from context
		repos := resolver.GetRepositories(p.Context)
		if repos == nil || repos.Records == nil {
			return nil, nil
		}

		// Query database
		rec, err := repos.Records.GetByURI(p.Context, uri)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil // Not found
			}
			return nil, fmt.Errorf("failed to fetch record: %w", err)
		}

		// Parse JSON to map
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(rec.JSON), &data); err != nil {
			return nil, fmt.Errorf("failed to parse record JSON: %w", err)
		}

		// Reserved record fields — always authoritative, unconditional.
		// The corresponding lexicon property (if any) was dropped in
		// buildRecordFields.
		data["uri"] = rec.URI
		data["cid"] = rec.CID
		data["did"] = rec.DID
		data["rkey"] = rec.RKey

		// Attach labels from every ingested labeler (best-effort).
		// The single-record resolver does not support a labelerDids
		// arg today — callers can post-filter client-side — so we
		// pass nil to get the full union.
		labelsByURI := loadLabelsByURI(p.Context, repos, nil, []*repositories.Record{rec})
		data["labels"] = labelsByURI[rec.URI]

		return data, nil
	}
}

// createCollectionStatsResolver creates a resolver for collection statistics.
func (b *Builder) createCollectionStatsResolver() graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		// Get repositories from context
		repos := resolver.GetRepositories(p.Context)
		if repos == nil || repos.Records == nil {
			return []interface{}{}, nil
		}

		// Extract optional collections filter
		var collections []string
		if collectionsArg, ok := p.Args["collections"].([]interface{}); ok {
			for _, c := range collectionsArg {
				if s, ok := c.(string); ok {
					collections = append(collections, s)
				}
			}
		}

		// Query database
		stats, err := repos.Records.GetCollectionStatsFiltered(p.Context, collections)
		if err != nil {
			return nil, fmt.Errorf("failed to get collection stats: %w", err)
		}

		// Convert to interface slice for GraphQL
		result := make([]interface{}, len(stats))
		for i, stat := range stats {
			result[i] = map[string]interface{}{
				"collection": stat.Collection,
				"count":      stat.Count,
			}
		}

		return result, nil
	}
}

// createCollectionTimeSeriesResolver creates a resolver for collection time series data.
func (b *Builder) createCollectionTimeSeriesResolver() graphql.FieldResolveFn {
	return func(p graphql.ResolveParams) (interface{}, error) {
		collection, ok := p.Args["collection"].(string)
		if !ok || collection == "" {
			return nil, fmt.Errorf("collection is required")
		}

		// Get repositories from context
		repos := resolver.GetRepositories(p.Context)
		if repos == nil || repos.Records == nil {
			return nil, nil
		}

		// Query database
		timeSeries, err := repos.Records.GetCollectionTimeSeries(p.Context, collection)
		if err != nil {
			return nil, fmt.Errorf("failed to get collection time series: %w", err)
		}

		// Convert data points to interface slice
		dataPoints := make([]interface{}, len(timeSeries.Data))
		for i, point := range timeSeries.Data {
			dataPoints[i] = map[string]interface{}{
				"date":       point.Date,
				"count":      point.Count,
				"cumulative": point.Cumulative,
			}
		}

		return map[string]interface{}{
			"collection":   timeSeries.Collection,
			"totalRecords": timeSeries.TotalRecords,
			"uniqueUsers":  timeSeries.UniqueUsers,
			"data":         dataPoints,
		}, nil
	}
}

// emptyConnection returns an empty Relay connection.
func emptyConnection() map[string]interface{} {
	return map[string]interface{}{
		"edges": []interface{}{},
		"pageInfo": map[string]interface{}{
			"hasNextPage":     false,
			"hasPreviousPage": false,
			"startCursor":     nil,
			"endCursor":       nil,
		},
		"totalCount": 0,
	}
}

// encodeCursor encodes a composite (indexed_at, uri) cursor as base64.
func encodeCursor(indexedAt, uri string) string {
	return base64.URLEncoding.EncodeToString([]byte(indexedAt + "|" + uri))
}

// decodeCursor decodes a base64 cursor into (indexed_at, uri) components.
func decodeCursor(cursor string) (string, string, error) {
	data, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", err
	}
	parts := strings.SplitN(string(data), "|", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("malformed cursor: expected 'timestamp|uri'")
	}
	return parts[0], parts[1], nil
}

// GetRecordType returns the GraphQL type for a record.
func (b *Builder) GetRecordType(lexiconID string) *graphql.Object {
	return b.recordTypes[lexiconID]
}

// GetConnectionType returns the connection type for a record.
func (b *Builder) GetConnectionType(lexiconID string) *graphql.Object {
	return b.connectionTypes[lexiconID]
}
