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
	"github.com/graphql-go/graphql/language/ast"

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
	recordTypes     map[string]*graphql.Object      // lexiconID -> record type
	connectionTypes map[string]*graphql.Object      // lexiconID -> connection type
	whereInputs     map[string]*graphql.InputObject // lexiconID -> WhereInput (issue #87)
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
		whereInputs:     make(map[string]*graphql.InputObject),
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

	// Phase 3b (issue #87): Build all WhereInputs up-front so the
	// joined-where injection in pass 2 can look up target lexicons
	// regardless of map iteration order. Today's lazy construction
	// inside buildQueryType doesn't work because the badge.award
	// WhereInput needs the badge.definition WhereInput to already
	// exist, and the order is non-deterministic.
	b.buildWhereInputTypes()

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

// buildWhereInputTypes constructs every per-collection WhereInput
// in two passes:
//   - Pass 1: build the input with property-derived fields, the
//     auto-added `did`, lexicon-specific filter-registry fields,
//     and `_and`/`_or`. Store in b.whereInputs.
//   - Pass 2: iterate joinedWhereRegistry (issue #87) and
//     arrayWhereRegistry (issue #88) and inject the nested-where
//     fields via *graphql.InputObject.AddFieldConfig, looking up
//     each joined target lexicon's WhereInput from b.whereInputs
//     and constructing each array-element WhereInput from the
//     parent's element def.
//
// The two-pass split is necessary because joined-where targets
// reference other lexicons' WhereInputs; building lazily inside
// buildQueryType (the previous approach) hit non-determinism on
// map iteration.
func (b *Builder) buildWhereInputTypes() {
	// Pass 1.
	for _, lex := range b.registry.GetCollectionLexicons() {
		input := buildWhereInputType(lex)
		if input != nil {
			b.whereInputs[lex.ID] = input
		}
	}

	// Pass 2: wire joined and array fields after all base inputs exist.
	for _, lex := range b.registry.GetCollectionLexicons() {
		parentInput := b.whereInputs[lex.ID]
		if parentInput == nil {
			continue
		}
		for _, jd := range joinedWhereRegistry[lex.ID] {
			targetInput := b.whereInputs[jd.TargetLexicon]
			if targetInput == nil {
				// Target lexicon not registered (e.g. dev hasn't
				// uploaded the target lexicon yet). Skip silently —
				// the absence of the field is the safe behaviour.
				// Log at warn so an operator can see why a
				// documented filter is missing.
				slog.Warn("joinedWhereRegistry entry references unregistered lexicon — skipping field",
					"parent", lex.ID, "field", jd.FieldName, "target", jd.TargetLexicon)
				continue
			}
			parentInput.AddFieldConfig(jd.FieldName, &graphql.InputObjectFieldConfig{
				Type:        targetInput,
				Description: jd.Description,
			})
		}

		// Issue #88: array-element nested-where injection. Each
		// descriptor synthesises a per-element WhereInput from the
		// parent's named element def. If the parent lexicon doesn't
		// expose the referenced element def (typically because dev
		// hasn't upgraded the parent lexicon to the version that
		// added it), buildArrayElementInputType returns nil — log
		// and skip, same shape as the joined-where unregistered-
		// target path.
		for _, ad := range arrayWhereRegistry[lex.ID] {
			elementInput := buildArrayElementInputType(lex, ad)
			if elementInput == nil {
				slog.Warn("arrayWhereRegistry entry references missing element def — skipping field",
					"parent", lex.ID, "field", ad.FieldName, "elementDef", ad.ElementDef)
				continue
			}
			parentInput.AddFieldConfig(ad.FieldName, &graphql.InputObjectFieldConfig{
				Type:        elementInput,
				Description: ad.Description,
			})
		}
	}
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
		"pds": &graphql.Field{
			Type:        graphql.String,
			Description: "Service endpoint of the PDS hosting the author's DID. Null if not yet resolved.",
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
	for k, v := range query.PDSFilterArgs() {
		genericRecordsArgs[k] = v
	}
	genericRecordsArgs["search"] = &graphql.ArgumentConfig{
		Type:        graphql.String,
		Description: "Full-text search across title, shortDescription, description, and workScope.",
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

		args := query.ConnectionArgs()

		// Per-collection WhereInput was already built in phase 3b
		// (with joined-where fields wired). Just look it up.
		if whereInput := b.whereInputs[lexiconID]; whereInput != nil {
			args["where"] = &graphql.ArgumentConfig{
				Type:        whereInput,
				Description: "Filter conditions (all combined with AND)",
			}
		}

		fields[fieldName] = &graphql.Field{
			Type:        connectionType,
			Description: fmt.Sprintf("Query %s records", lexiconID),
			Args:        args,
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
// Supports forward (first/after) and backward (last/before) pagination,
// custom sorting (orderBy/orderDirection), field filters (where), and totalCount.
func (b *Builder) resolveRecordConnection(
	p graphql.ResolveParams,
	collection string,
	buildNode nodeBuilder,
) (interface{}, error) {
	repos := resolver.GetRepositories(p.Context)
	if repos == nil || repos.Records == nil {
		return emptyConnection(), nil
	}

	// Extract pagination args.
	rawFirst, hasFirst := p.Args["first"].(int)
	rawLast, hasLast := p.Args["last"].(int)
	after, _ := p.Args["after"].(string)
	before, _ := p.Args["before"].(string)

	// Validate: cannot mix forward and backward.
	isBackward := hasLast || before != ""
	isForward := hasFirst || after != ""
	if isForward && isBackward {
		return nil, fmt.Errorf("cannot mix forward (first/after) and backward (last/before) pagination")
	}

	// Determine page size.
	var pageSize int
	if isBackward {
		pageSize = query.ClampPageSize(rawLast)
	} else {
		pageSize = query.ClampPageSize(rawFirst)
	}

	// Extract sort options.
	sortField, _ := p.Args["orderBy"].(string)
	if sortField == "" {
		sortField = "indexed_at"
	}
	sortDirStr, _ := p.Args["orderDirection"].(string)
	if sortDirStr == "" {
		sortDirStr = "DESC"
	}
	sortOpt := &repositories.SortOption{
		Field:     sortField,
		Direction: repositories.SortDirection(sortDirStr),
	}

	// Decode cursor if provided.
	var cursorSortValue, cursorURI string
	cursorStr := after
	if isBackward {
		cursorStr = before
	}
	if cursorStr != "" {
		cursorSortField, sv, u, err := decodeCursorV2(cursorStr)
		if err != nil {
			return nil, fmt.Errorf("invalid cursor: %w", err)
		}
		// Validate cursor sort field matches current orderBy.
		if cursorSortField != sortField {
			return nil, fmt.Errorf("cursor is incompatible with requested sort order (cursor: %s, orderBy: %s); please restart pagination", cursorSortField, sortField)
		}
		cursorSortValue = sv
		cursorURI = u
	}

	// Build filters.
	labelFilter := parseLabelFilter(p.Args)
	authorsFilter, err := query.ParseAuthorsFilter(p.Args)
	if err != nil {
		if errors.Is(err, repositories.ErrAuthorsFilterTooLarge) ||
			strings.Contains(err.Error(), "exceeds maximum") {
			metrics.RecordAuthorsFilterTooLarge()
		}
		return nil, fmt.Errorf("invalid authors argument: %w", err)
	}
	searchFilter := query.ParseSearchFilter(p.Args)
	pdsExclude, err := query.ParsePDSExcludeFilter(p.Args)
	if err != nil {
		return nil, fmt.Errorf("invalid excludePds argument: %w", err)
	}

	filter := repositories.RecordFilter{Labels: labelFilter, Search: searchFilter, PDSExclude: pdsExclude}
	if authorsFilter != nil {
		filter.Authors = *authorsFilter
		if len(*authorsFilter) == 0 {
			metrics.RecordAuthorsFilterEmptyBlocked()
		} else {
			metrics.RecordAuthorsFilterApplied(collection, len(*authorsFilter))
		}
	}

	// Extract field filters from `where` argument.
	var filterGroup repositories.FilterGroup
	if whereArg, ok := p.Args["where"]; ok && whereArg != nil {
		lex, _ := b.registry.GetLexicon(collection)
		filterGroup, err = extractFieldFilters(whereArg, lex, b.registry)
		if err != nil {
			return nil, fmt.Errorf("invalid where filter: %w", err)
		}
	}

	// For now, sorting is still using indexed_at keyset cursor through
	// the existing GetByCollectionFiltered method. Full sort-aware keyset
	// pagination (Phase 2.5 of the plan) requires more repository changes.
	// This wires up the sort arguments and cursor format; the SQL sort
	// is applied when sortField == "indexed_at" (default case).
	//
	// TODO: Add GetByCollectionSortedWithKeysetCursor for non-default sorts.
	records, err := repos.Records.GetByCollectionFiltered(
		p.Context, collection, pageSize+1, cursorSortValue, cursorURI, filter, sortOpt, &filterGroup,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query records: %w", err)
	}

	// Determine pagination flags.
	hasMore := len(records) > pageSize
	if hasMore {
		records = records[:pageSize]
	}

	// For backward pagination: reverse the result order.
	if isBackward {
		for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
			records[i], records[j] = records[j], records[i]
		}
	}

	// Batch-load active labels.
	labelsByURI := loadLabelsByURI(p.Context, repos, labelFilter.LabelerSrcs, records)

	// Build edges.
	edges := make([]interface{}, 0, len(records))
	var startCursor, endCursor string

	for _, rec := range records {
		var value map[string]interface{}
		if err := json.Unmarshal([]byte(rec.JSON), &value); err != nil {
			continue
		}

		node, ok := buildNode(rec, value)
		if !ok {
			continue
		}

		if nodeMap, ok := node.(map[string]interface{}); ok {
			nodeMap["labels"] = labelsByURI[rec.URI]
			// pds is nullable in the schema. Empty string means
			// "actor row had no resolved pds" — surface as GraphQL
			// null so clients can distinguish "unknown" from "set".
			if rec.PDS != "" {
				nodeMap["pds"] = rec.PDS
			} else {
				nodeMap["pds"] = nil
			}
		}

		cursor := encodeCursorV2(sortField, rec.IndexedAt.Format("2006-01-02T15:04:05Z"), rec.URI)
		if startCursor == "" {
			startCursor = cursor
		}
		endCursor = cursor

		edges = append(edges, map[string]interface{}{
			"cursor": cursor,
			"node":   node,
		})
	}

	// Page info.
	var hasNextPage, hasPreviousPage bool
	if isBackward {
		hasPreviousPage = hasMore
		hasNextPage = before != ""
	} else {
		hasNextPage = hasMore
		hasPreviousPage = after != ""
	}

	result := map[string]interface{}{
		"edges": edges,
		"pageInfo": map[string]interface{}{
			"hasNextPage":     hasNextPage,
			"hasPreviousPage": hasPreviousPage,
			"startCursor":     startCursor,
			"endCursor":       endCursor,
		},
	}

	// totalCount: only compute if requested (check AST).
	if isTotalCountRequested(p) {
		count, err := repos.Records.GetCollectionCount(p.Context, collection)
		if err != nil {
			slog.Warn("Failed to compute totalCount", "collection", collection, "error", err)
			// Return nil (nullable) rather than failing the whole query.
		} else {
			result["totalCount"] = count
		}
	}

	return result, nil
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
	// Look up the record def once per collection, not per record.
	def, _ := b.registry.GetRecordDef(lexiconID)

	return func(p graphql.ResolveParams) (interface{}, error) {
		return b.resolveRecordConnection(p, lexiconID,
			func(rec *repositories.Record, data map[string]interface{}) (interface{}, bool) {
				// Sanitize record against lexicon schema. Returns nil
				// if required fields are missing — skip the record
				// silently instead of letting NonNull propagation kill
				// the entire query response.
				sanitized := lexicon.SanitizeRecord(def, b.registry, data)
				if sanitized == nil {
					return nil, false
				}
				// Reserved record fields — always our metadata.
				sanitized["uri"] = rec.URI
				sanitized["cid"] = rec.CID
				sanitized["did"] = rec.DID
				sanitized["rkey"] = rec.RKey
				// pds is set by resolveRecordConnection alongside
				// labels (both come from a join, not the lexicon
				// body). Leaving it unset here would shadow the
				// later assignment with nil, so we don't touch it.
				return sanitized, true
			})
	}
}

// createSingleRecordResolver creates a resolver for fetching a single record.
func (b *Builder) createSingleRecordResolver(lexiconID string) graphql.FieldResolveFn {
	def, _ := b.registry.GetRecordDef(lexiconID)

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

		// Sanitize record against lexicon schema.
		sanitized := lexicon.SanitizeRecord(def, b.registry, data)
		if sanitized == nil {
			return nil, nil // Record has missing required fields — treat as not found.
		}

		// Reserved record fields — always authoritative.
		sanitized["uri"] = rec.URI
		sanitized["cid"] = rec.CID
		sanitized["did"] = rec.DID
		sanitized["rkey"] = rec.RKey
		if rec.PDS != "" {
			sanitized["pds"] = rec.PDS
		} else {
			sanitized["pds"] = nil
		}

		// Attach labels from every ingested labeler (best-effort).
		labelsByURI := loadLabelsByURI(p.Context, repos, nil, []*repositories.Record{rec})
		sanitized["labels"] = labelsByURI[rec.URI]

		return sanitized, nil
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

// isTotalCountRequested checks the GraphQL query AST to determine if the
// client selected the totalCount field. Only runs the COUNT query when true.
func isTotalCountRequested(p graphql.ResolveParams) bool {
	for _, field := range p.Info.FieldASTs {
		if field.SelectionSet == nil {
			continue
		}
		for _, sel := range field.SelectionSet.Selections {
			if f, ok := sel.(*ast.Field); ok && f.Name.Value == "totalCount" {
				return true
			}
		}
	}
	return false
}

// encodeCursorV2 encodes a cursor as ["sortField", "sortValue", "uri"] in base64-URL.
func encodeCursorV2(sortField, sortValue, uri string) string {
	arr := []string{sortField, sortValue, uri}
	data, _ := json.Marshal(arr)
	return base64.URLEncoding.EncodeToString(data)
}

// decodeCursorV2 decodes a cursor, supporting both new JSON array format
// and legacy pipe-delimited format.
// Returns (sortField, sortValue, uri, error).
func decodeCursorV2(cursor string) (string, string, string, error) {
	data, err := base64.URLEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", "", err
	}

	// Try new JSON array format first: ["sortField", "sortValue", "uri"]
	var arr []string
	if json.Unmarshal(data, &arr) == nil && len(arr) == 3 {
		return arr[0], arr[1], arr[2], nil
	}

	// Fall back to legacy pipe-delimited format: "timestamp|uri"
	parts := strings.SplitN(string(data), "|", 2)
	if len(parts) != 2 {
		return "", "", "", fmt.Errorf("malformed cursor")
	}
	return "indexed_at", parts[0], parts[1], nil
}

// GetRecordType returns the GraphQL type for a record.
func (b *Builder) GetRecordType(lexiconID string) *graphql.Object {
	return b.recordTypes[lexiconID]
}

// GetConnectionType returns the connection type for a record.
func (b *Builder) GetConnectionType(lexiconID string) *graphql.Object {
	return b.connectionTypes[lexiconID]
}
