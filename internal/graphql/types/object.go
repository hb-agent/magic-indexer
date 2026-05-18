package types //nolint:revive // package name is descriptive within graphql context

import (
	"fmt"
	"log/slog"
	"unicode"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/lexicon"
)

// ReservedRecordFields are field names that are always injected as
// record metadata. If a lexicon happens to define a property with one
// of these names, we drop it with a warning rather than clobbering
// the injected value at resolve time — clients need to be able to
// rely on these fields always being present with the canonical type.
//
// Imported from hypercerts-org/hyperindex#34 (filter-feature) which
// established the same policy there for the same reasons.
var ReservedRecordFields = map[string]bool{
	"uri":    true,
	"cid":    true,
	"did":    true,
	"rkey":   true,
	"labels": true,
	"pds":    true,
}

// maxResolveDepth bounds how deep the field-building walker will
// descend through nested lexicon refs before falling back to
// graphql.String. Cycles are already handled by the
// cache-before-thunk pattern in BuildObjectType / BuildRecordType —
// a ref the builder is already materialising resolves via the
// cache on the second visit without recursing. This limit is an
// extra guard against pathological lexicons (e.g., an admin
// uploading a chain of 200 refs through uploadLexicons) that
// would otherwise blow the stack during schema construction.
// Real lexicons we ship nest no more than a handful of levels.
const maxResolveDepth = 64

// ObjectBuilder builds GraphQL object types from lexicon definitions.
type ObjectBuilder struct {
	mapper   *Mapper
	registry *lexicon.Registry
	// depth tracks the current resolveRefType recursion depth.
	// It is not guarded by a mutex because the schema builder runs
	// single-threaded during startup; if that ever changes, this
	// needs to move into a per-call parameter.
	depth int
	// derivedFieldsByLexicon maps lexiconID → fieldName → field for
	// synthetic record-level fields (e.g. awardCount, issue #89)
	// that don't come from the lexicon's own properties. Nil-safe —
	// the per-record builder treats nil as no derived fields.
	//
	// Owned by the schema package's derivedFieldRegistry; passed in
	// at construction so this package stays free of schema-layer
	// imports.
	derivedFieldsByLexicon map[string]map[string]*graphql.Field
}

// NewObjectBuilder creates a new object builder with no derived
// fields. Legacy ctor — kept stable for the 5 existing test sites
// that construct ObjectBuilder directly. Production code should
// use NewObjectBuilderWithDerivedFields.
func NewObjectBuilder(mapper *Mapper, registry *lexicon.Registry) *ObjectBuilder {
	return NewObjectBuilderWithDerivedFields(mapper, registry, nil)
}

// NewObjectBuilderWithDerivedFields constructs a builder that
// injects the given per-lexicon synthetic fields into each
// per-collection record type after the lexicon-property loop.
// The schema-package Builder uses this ctor with the registry-
// derived map (see internal/graphql/schema/derived_fields.go).
func NewObjectBuilderWithDerivedFields(mapper *Mapper, registry *lexicon.Registry, derivedFieldsByLexicon map[string]map[string]*graphql.Field) *ObjectBuilder {
	return &ObjectBuilder{
		mapper:                 mapper,
		registry:               registry,
		derivedFieldsByLexicon: derivedFieldsByLexicon,
	}
}

// BuildObjectType builds a GraphQL object type from an ObjectDef.
// The ref is the fully-qualified reference (e.g., "org.hypercerts.defs#uri").
func (b *ObjectBuilder) BuildObjectType(ref string, def *lexicon.ObjectDef) *graphql.Object {
	// Check cache first
	if t, ok := b.mapper.GetObjectType(ref); ok {
		return t
	}

	// Generate GraphQL type name from ref
	typeName := refToTypeName(ref)

	// Create the object type with a thunk to handle circular references
	obj := graphql.NewObject(graphql.ObjectConfig{
		Name:        typeName,
		Description: fmt.Sprintf("Object type for %s", ref),
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			return b.buildFields(ref, def)
		}),
	})

	// Cache before building fields (for circular refs)
	b.mapper.SetObjectType(ref, obj)

	return obj
}

// BuildRecordType builds a GraphQL object type from a RecordDef.
// The lexiconID is the NSID (e.g., "org.hypercerts.claim.activity").
func (b *ObjectBuilder) BuildRecordType(lexiconID string, def *lexicon.RecordDef) *graphql.Object {
	// Check cache first
	if t, ok := b.mapper.GetObjectType(lexiconID); ok {
		return t
	}

	typeName := lexicon.ToTypeName(lexiconID)

	obj := graphql.NewObject(graphql.ObjectConfig{
		Name:        typeName,
		Description: fmt.Sprintf("Record type for %s", lexiconID),
		Fields: graphql.FieldsThunk(func() graphql.Fields {
			return b.buildRecordFields(lexiconID, def)
		}),
	})

	b.mapper.SetObjectType(lexiconID, obj)

	return obj
}

// buildFields builds GraphQL fields from ObjectDef properties.
func (b *ObjectBuilder) buildFields(contextRef string, def *lexicon.ObjectDef) graphql.Fields {
	fields := graphql.Fields{}

	// Extract lexicon ID from context ref for resolving local refs
	contextLexiconID := lexicon.IDFromRef(contextRef)

	for _, entry := range def.Properties {
		field := b.buildField(contextLexiconID, entry.Name, &entry.Property, def.IsRequired(entry.Name))
		if field != nil {
			fields[entry.Name] = field
		}
	}

	return fields
}

// buildRecordFields builds GraphQL fields from RecordDef properties.
// The synthesised metadata fields (uri, cid, did, rkey, labels, pds)
// are always present on every record type with their canonical Go
// types. Any lexicon property whose name collides with one of those
// fields is skipped with a warning — the alternative of letting a
// lexicon author shadow a type-critical field like `uri` leads to
// subtle schema mismatches that are harder to debug than a startup
// warning.
func (b *ObjectBuilder) buildRecordFields(lexiconID string, def *lexicon.RecordDef) graphql.Fields {
	fields := graphql.Fields{
		"uri": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "AT-URI of this record",
		},
		"cid": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "CID of this record version",
		},
		"did": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "DID of the record author",
		},
		"rkey": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.String),
			Description: "Record key (last segment of the AT-URI)",
		},
		"labels": &graphql.Field{
			Type:        graphql.NewNonNull(graphql.NewList(graphql.NewNonNull(graphql.String))),
			Description: "Active label values on this record from any ingested labeler. Always a list (possibly empty), never null.",
		},
		"pds": &graphql.Field{
			Type:        graphql.String,
			Description: "Service endpoint of the PDS hosting the author's DID, resolved from the DID document. Null if the author's PDS has not yet been resolved (e.g. backfill in progress, or transient resolver failure). Use this to identify records authored from a particular PDS — for example, to flag test-PDS content in the UI when consumers opt into seeing it via excludePds being absent. NOTE: GraphQL subscription record events do not populate this field (it is always null on the *Events streams), since the record's actor row may not yet exist when the subscription fan-out fires; query the connection field on a follow-up read to obtain the resolved PDS.",
		},
	}

	// Build required set for quick lookup
	requiredSet := make(map[string]bool)
	for _, prop := range def.Properties {
		if prop.Property.Required {
			requiredSet[prop.Name] = true
		}
	}

	for _, entry := range def.Properties {
		if ReservedRecordFields[entry.Name] {
			slog.Warn("Skipping lexicon property that collides with a reserved record field",
				"lexicon", lexiconID, "property", entry.Name)
			continue
		}
		field := b.buildField(lexiconID, entry.Name, &entry.Property, requiredSet[entry.Name])
		if field != nil {
			fields[entry.Name] = field
		}
	}

	// Inject synthetic record-level fields from the derived-fields
	// registry (issue #89). Lexicon properties take precedence —
	// a derived field that collides with a lexicon property is
	// silently skipped with a warn log, mirroring the
	// ReservedRecordFields policy above. (Collisions with the
	// reserved metadata fields panic at registry init time, see
	// internal/graphql/schema/derived_fields.go MustNotReserveField.)
	for fieldName, field := range b.derivedFieldsByLexicon[lexiconID] {
		if _, collide := fields[fieldName]; collide {
			slog.Warn("Derived field collides with lexicon property — keeping lexicon property",
				"lexicon", lexiconID, "field", fieldName)
			continue
		}
		fields[fieldName] = field
	}

	return fields
}

// buildField builds a single GraphQL field from a property.
func (b *ObjectBuilder) buildField(contextLexiconID, name string, prop *lexicon.Property, required bool) *graphql.Field {
	var fieldType graphql.Output

	switch prop.Type {
	case lexicon.TypeRef:
		fieldType = b.resolveRefType(contextLexiconID, prop.Ref)
	case lexicon.TypeUnion:
		fieldType = b.buildUnionType(contextLexiconID, name, prop.Refs)
	case lexicon.TypeArray:
		itemType := b.resolveArrayItemType(contextLexiconID, prop.Items)
		fieldType = graphql.NewList(graphql.NewNonNull(itemType))
	default:
		fieldType = b.mapper.MapPrimitiveType(prop.Type, prop.Format)
	}

	if fieldType == nil {
		// Fallback to String for unknown types
		fieldType = graphql.String
	}

	if required {
		fieldType = graphql.NewNonNull(fieldType)
	}

	return &graphql.Field{
		Type:        fieldType,
		Description: prop.Description,
	}
}

// resolveRefType resolves a ref to a GraphQL type.
func (b *ObjectBuilder) resolveRefType(contextLexiconID, ref string) graphql.Output {
	if ref == "" {
		return graphql.String
	}

	// Resolve local refs
	fullRef := ref
	if lexicon.IsLocalRef(ref) {
		fullRef = lexicon.ResolveLocalRef(ref, contextLexiconID)
	}

	// Check if already built
	if t, ok := b.mapper.GetObjectType(fullRef); ok {
		return t
	}

	// Bound recursion depth as a defence against hostile uploaded
	// lexicons. Cycles are handled by the cache above (a second
	// visit to the same ref hits the cache); this limit only
	// trips on genuinely unbounded depth.
	if b.depth >= maxResolveDepth {
		slog.Warn("Lexicon ref resolution exceeded depth limit; falling back to String",
			"ref", ref, "fullRef", fullRef, "depth", b.depth, "limit", maxResolveDepth)
		return graphql.String
	}
	b.depth++
	defer func() { b.depth-- }()

	// Try to resolve from registry
	resolved, ok := b.registry.ResolveRef(ref, contextLexiconID)
	if !ok {
		// Unknown ref - return String as fallback
		return graphql.String
	}

	// Build the type based on what we resolved
	switch def := resolved.(type) {
	case *lexicon.ObjectDef:
		return b.BuildObjectType(fullRef, def)
	case *lexicon.RecordDef:
		resolvedLexiconID := lexicon.IDFromRef(fullRef)
		return b.BuildRecordType(resolvedLexiconID, def)
	default:
		return graphql.String
	}
}

// buildUnionType builds a GraphQL union type from refs.
func (b *ObjectBuilder) buildUnionType(contextLexiconID, fieldName string, refs []string) graphql.Output {
	if len(refs) == 0 {
		return graphql.String
	}

	// Handle string-type refs (primitive unions)
	// These are refs to primitive types like "contributorIdentity" which is just a string
	var objectTypes []*graphql.Object
	hasPrimitives := false

	for _, ref := range refs {
		fullRef := ref
		if lexicon.IsLocalRef(ref) {
			fullRef = lexicon.ResolveLocalRef(ref, contextLexiconID)
		}

		resolved, ok := b.registry.ResolveRef(ref, contextLexiconID)
		if !ok {
			// Check if it's a primitive type ref (like #contributorIdentity -> string)
			hasPrimitives = true
			continue
		}

		switch def := resolved.(type) {
		case *lexicon.ObjectDef:
			// A zero-property ObjectDef means the parser routed a
			// non-object type (e.g. `{"type": "string"}`) through
			// parseObjectDef. graphql-go refuses to register an
			// object type with no fields, so fold it in with the
			// other primitive variants and let the union collapse
			// to JSONScalar below.
			if len(def.Properties) == 0 {
				hasPrimitives = true
				continue
			}
			objType := b.BuildObjectType(fullRef, def)
			objectTypes = append(objectTypes, objType)
		case *lexicon.RecordDef:
			resolvedLexiconID := lexicon.IDFromRef(fullRef)
			objType := b.BuildRecordType(resolvedLexiconID, def)
			objectTypes = append(objectTypes, objType)
		default:
			hasPrimitives = true
		}
	}

	// If we only have primitives, return JSON scalar
	if len(objectTypes) == 0 {
		return JSONScalar
	}

	// If we have a mix, use JSON as fallback for now
	// (proper handling would need interface types)
	if hasPrimitives {
		return JSONScalar
	}

	// Create union name from context and field
	unionName := lexicon.ToTypeName(contextLexiconID) + capitalizeFirst(fieldName)

	// Disambiguate when a def-derived object type already carries this
	// name. v0.12.0 of the hypercerts lexicon hit this on
	// `org.hypercerts.claim.activity`: `#contributorIdentity` is a
	// top-level object def (object type name
	// `OrgHypercertsClaimActivityContributorIdentity`) AND the
	// `contributor.contributorIdentity` field is a union that maps to
	// the same generated name. graphql-go rejects schemas with
	// duplicate type names, so we suffix the union with `Union`. The
	// suffix is applied only on collision so existing unions in the
	// schema keep their stable names.
	if b.mapper.HasObjectTypeNamed(unionName) {
		unionName += "Union"
	}

	// Check if union already exists
	if u, ok := b.mapper.GetUnionType(unionName); ok {
		return u
	}

	// Build union type
	union := graphql.NewUnion(graphql.UnionConfig{
		Name:        unionName,
		Description: fmt.Sprintf("Union type for %s.%s", contextLexiconID, fieldName),
		Types:       objectTypes,
		ResolveType: func(p graphql.ResolveTypeParams) *graphql.Object {
			// Resolve based on $type field in the data
			data, ok := p.Value.(map[string]interface{})
			if !ok {
				if len(objectTypes) > 0 {
					return objectTypes[0]
				}
				return nil
			}
			typeVal, hasType := data["$type"].(string)
			if hasType {
				// Find matching object type
				for _, objType := range objectTypes {
					// Match by type name
					if refToTypeName(typeVal) == objType.Name() {
						return objType
					}
				}
			}
			// Default to first type
			if len(objectTypes) > 0 {
				return objectTypes[0]
			}
			return nil
		},
	})

	b.mapper.SetUnionType(unionName, union)
	return union
}

// resolveArrayItemType resolves the item type for an array.
func (b *ObjectBuilder) resolveArrayItemType(contextLexiconID string, items *lexicon.ArrayItems) graphql.Output {
	if items == nil {
		return graphql.String
	}

	switch items.Type {
	case lexicon.TypeRef:
		return b.resolveRefType(contextLexiconID, items.Ref)
	case lexicon.TypeUnion:
		return b.buildUnionType(contextLexiconID, "items", items.Refs)
	default:
		return b.mapper.MapPrimitiveType(items.Type, "")
	}
}

// refToTypeName converts a ref to a GraphQL type name.
func refToTypeName(ref string) string {
	lexiconID, defName, ok := lexicon.ParseRef(ref)
	if !ok || defName == "" {
		return lexicon.ToTypeName(ref)
	}
	// For refs like "org.hypercerts.defs#uri", create "OrgHypercertsDefsUri"
	return lexicon.ToTypeName(lexiconID) + capitalizeFirst(defName)
}

// capitalizeFirst capitalizes the first letter of a string.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}
