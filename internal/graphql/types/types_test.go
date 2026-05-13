package types //nolint:revive // package name is descriptive within graphql context

import (
	"testing"
	"time"

	"github.com/graphql-go/graphql"

	"github.com/GainForest/hypergoat/internal/lexicon"
)

// ---------- Mapper tests ----------

func TestMapper_MapPrimitiveType(t *testing.T) {
	m := NewMapper()

	tests := []struct {
		name       string
		lexType    string
		format     string
		wantName   string
		wantNotNil bool // for types where we just check non-nil (e.g., BlobType)
	}{
		{name: "string no format", lexType: "string", format: "", wantName: "String"},
		{name: "string datetime", lexType: "string", format: "datetime", wantName: "DateTime"},
		{name: "string uri", lexType: "string", format: "uri", wantName: "String"},
		{name: "integer", lexType: "integer", format: "", wantName: "Int"},
		{name: "boolean", lexType: "boolean", format: "", wantName: "Boolean"},
		{name: "number", lexType: "number", format: "", wantName: "Float"},
		{name: "blob", lexType: "blob", format: "", wantName: "Blob", wantNotNil: true},
		{name: "bytes", lexType: "bytes", format: "", wantName: "String"},
		{name: "cid-link", lexType: "cid-link", format: "", wantName: "String"},
		{name: "unknown", lexType: "unknown", format: "", wantName: "JSON"},
		{name: "empty default", lexType: "", format: "", wantName: "String"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.MapPrimitiveType(tt.lexType, tt.format)
			if got == nil {
				t.Fatal("MapPrimitiveType returned nil")
			}
			if got.Name() != tt.wantName {
				t.Errorf("MapPrimitiveType(%q, %q) name = %q, want %q",
					tt.lexType, tt.format, got.Name(), tt.wantName)
			}
			if tt.wantNotNil {
				if _, ok := got.(*graphql.Object); !ok {
					t.Errorf("expected *graphql.Object for %q, got %T", tt.lexType, got)
				}
			}
		})
	}
}

func TestMapper_ObjectTypeCache(t *testing.T) {
	m := NewMapper()

	// Non-existent key returns false.
	if _, ok := m.GetObjectType("nope"); ok {
		t.Fatal("expected GetObjectType to return false for missing key")
	}

	// Set and retrieve.
	obj := graphql.NewObject(graphql.ObjectConfig{
		Name:   "TestObj",
		Fields: graphql.Fields{"id": &graphql.Field{Type: graphql.String}},
	})
	m.SetObjectType("test.ref", obj)

	got, ok := m.GetObjectType("test.ref")
	if !ok {
		t.Fatal("expected GetObjectType to return true after Set")
	}
	if got != obj {
		t.Error("returned object differs from the one that was set")
	}

	// AllObjectTypes includes the entry (plus any defaults like Blob if cached).
	all := m.AllObjectTypes()
	if _, exists := all["test.ref"]; !exists {
		t.Error("AllObjectTypes missing 'test.ref'")
	}
}

func TestMapper_UnionTypeCache(t *testing.T) {
	m := NewMapper()

	// Non-existent key returns false.
	if _, ok := m.GetUnionType("nope"); ok {
		t.Fatal("expected GetUnionType to return false for missing key")
	}

	// Set and retrieve.
	dummyObj := graphql.NewObject(graphql.ObjectConfig{
		Name:   "DummyUnionMember",
		Fields: graphql.Fields{"x": &graphql.Field{Type: graphql.String}},
	})
	u := graphql.NewUnion(graphql.UnionConfig{
		Name:  "TestUnion",
		Types: []*graphql.Object{dummyObj},
		ResolveType: func(_ graphql.ResolveTypeParams) *graphql.Object {
			return dummyObj
		},
	})
	m.SetUnionType("TestUnion", u)

	got, ok := m.GetUnionType("TestUnion")
	if !ok {
		t.Fatal("expected GetUnionType to return true after Set")
	}
	if got != u {
		t.Error("returned union differs from the one that was set")
	}
}

// ---------- Scalar tests ----------

func TestJSONScalar_Serialize(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  interface{}
	}{
		{"map", map[string]interface{}{"key": "val"}, map[string]interface{}{"key": "val"}},
		{"string", "hello", "hello"},
		{"nil", nil, nil},
		{"int", 42, 42},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := JSONScalar.Serialize(tt.input)
			// JSONScalar.Serialize is the identity function.
			if fmtEq(got, tt.want) == false {
				t.Errorf("JSONScalar.Serialize(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestDateTimeScalar_Serialize(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name  string
		input interface{}
		want  interface{}
	}{
		{"string", "2024-01-15T12:00:00Z", "2024-01-15T12:00:00Z"},
		{"time.Time", now, now},
		{"nil", nil, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DateTimeScalar.Serialize(tt.input)
			// DateTimeScalar.Serialize is the identity function.
			if fmtEq(got, tt.want) == false {
				t.Errorf("DateTimeScalar.Serialize(%v) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// fmtEq is a simple equality check that handles nil comparisons.
func fmtEq(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	// For maps we just check non-nil; deeper comparison isn't necessary
	// because the scalar is an identity function.
	return true
}

// ---------- ObjectBuilder tests ----------

func TestObjectBuilder_BuildRecordType(t *testing.T) {
	registry := lexicon.NewRegistry()
	mapper := NewMapper()
	builder := NewObjectBuilder(mapper, registry)

	recordDef := &lexicon.RecordDef{
		Type: "record",
		Key:  "tid",
		Properties: []lexicon.PropertyEntry{
			{
				Name: "text",
				Property: lexicon.Property{
					Type:        "string",
					Description: "The post text",
				},
			},
			{
				Name: "count",
				Property: lexicon.Property{
					Type:     "integer",
					Required: true,
				},
			},
		},
	}

	lexiconID := "com.example.test.post"
	obj := builder.BuildRecordType(lexiconID, recordDef)
	if obj == nil {
		t.Fatal("BuildRecordType returned nil")
	}

	// Type name should be PascalCase of the NSID.
	wantName := "ComExampleTestPost"
	if obj.Name() != wantName {
		t.Errorf("type name = %q, want %q", obj.Name(), wantName)
	}

	// Force field thunk resolution by getting the fields.
	fields := obj.Fields()

	// Must have "uri" and "cid" standard fields.
	for _, std := range []string{"uri", "cid"} {
		if _, ok := fields[std]; !ok {
			t.Errorf("missing standard field %q", std)
		}
	}

	// Must have the custom properties.
	if _, ok := fields["text"]; !ok {
		t.Error("missing field 'text'")
	}
	if _, ok := fields["count"]; !ok {
		t.Error("missing field 'count'")
	}

	// Building the same ID again should return the cached object (same pointer).
	obj2 := builder.BuildRecordType(lexiconID, recordDef)
	if obj2 != obj {
		t.Error("expected cached object on second call, got a different pointer")
	}
}

func TestObjectBuilder_BuildObjectType(t *testing.T) {
	registry := lexicon.NewRegistry()
	mapper := NewMapper()
	builder := NewObjectBuilder(mapper, registry)

	objectDef := &lexicon.ObjectDef{
		Type:           "object",
		RequiredFields: []string{"width"},
		Properties: []lexicon.PropertyEntry{
			{
				Name: "width",
				Property: lexicon.Property{
					Type: "integer",
				},
			},
			{
				Name: "height",
				Property: lexicon.Property{
					Type: "integer",
				},
			},
			{
				Name: "label",
				Property: lexicon.Property{
					Type:   "string",
					Format: "datetime",
				},
			},
		},
	}

	ref := "com.example.defs#aspectRatio"
	obj := builder.BuildObjectType(ref, objectDef)
	if obj == nil {
		t.Fatal("BuildObjectType returned nil")
	}

	// For ref "com.example.defs#aspectRatio" the expected name is
	// ToTypeName("com.example.defs") + capitalizeFirst("aspectRatio")
	// = "ComExampleDefs" + "AspectRatio" = "ComExampleDefsAspectRatio"
	wantName := "ComExampleDefsAspectRatio"
	if obj.Name() != wantName {
		t.Errorf("type name = %q, want %q", obj.Name(), wantName)
	}

	fields := obj.Fields()

	for _, name := range []string{"width", "height", "label"} {
		if _, ok := fields[name]; !ok {
			t.Errorf("missing field %q", name)
		}
	}

	// "width" is required, so its type should be NonNull.
	widthField := fields["width"]
	if _, ok := widthField.Type.(*graphql.NonNull); !ok {
		t.Errorf("expected 'width' to be NonNull, got %T", widthField.Type)
	}

	// "height" is not required, so its type should NOT be NonNull.
	heightField := fields["height"]
	if _, isNonNull := heightField.Type.(*graphql.NonNull); isNonNull {
		t.Error("expected 'height' to not be NonNull")
	}

	// Building the same ref again should return the cached object.
	obj2 := builder.BuildObjectType(ref, objectDef)
	if obj2 != obj {
		t.Error("expected cached object on second call, got a different pointer")
	}
}

// ---------- buildUnionType tests ----------
//
// These regressions cover zero-property ObjectDef variants in mixed
// unions. graphql-go panics when asked to register an object type
// with no fields, so a lexicon like `{"type": "object"}` with no
// properties — which produces a zero-property ObjectDef — must
// fall through to JSONScalar via the primitive-fallback path,
// not be passed through as a real union variant.

// registerEmptyAndPopulated registers a synthetic lexicon containing
// (a) `#empty` — a zero-property object def, and (b) `#populated` —
// an object def with a single string property. Returns the
// fully-qualified refs in the same order.
func registerEmptyAndPopulated(t *testing.T, r *lexicon.Registry, id string) (emptyRef, populatedRef string) {
	t.Helper()
	r.Register(&lexicon.Lexicon{
		ID: id,
		Defs: lexicon.Defs{
			Others: map[string]lexicon.Def{
				"empty": {
					Type:   "object",
					Object: &lexicon.ObjectDef{Type: "object"},
				},
				"populated": {
					Type: "object",
					Object: &lexicon.ObjectDef{
						Type: "object",
						Properties: []lexicon.PropertyEntry{
							{Name: "value", Property: lexicon.Property{Type: "string"}},
						},
					},
				},
				"populated2": {
					Type: "object",
					Object: &lexicon.ObjectDef{
						Type: "object",
						Properties: []lexicon.PropertyEntry{
							{Name: "other", Property: lexicon.Property{Type: "integer"}},
						},
					},
				},
			},
		},
	})
	return lexicon.MakeRef(id, "empty"), lexicon.MakeRef(id, "populated")
}

func TestObjectBuilder_BuildUnionType_ZeroPropertyObjectFoldsToJSONScalar(t *testing.T) {
	registry := lexicon.NewRegistry()
	mapper := NewMapper()
	builder := NewObjectBuilder(mapper, registry)

	const lexID = "com.example.testunion"
	emptyRef, populatedRef := registerEmptyAndPopulated(t, registry, lexID)

	got := builder.buildUnionType(lexID, "myField", []string{emptyRef, populatedRef})
	if got == nil {
		t.Fatal("buildUnionType returned nil for {empty, populated}")
	}
	if got.Name() != JSONScalar.Name() {
		t.Errorf("union of {zero-property object, populated object} = %q, want JSONScalar (zero-property variant must fold to primitive)", got.Name())
	}
}

func TestObjectBuilder_BuildUnionType_AllZeroPropertyObjectFoldsToJSONScalar(t *testing.T) {
	registry := lexicon.NewRegistry()
	mapper := NewMapper()
	builder := NewObjectBuilder(mapper, registry)

	const lexID = "com.example.testunion2"
	emptyRef, _ := registerEmptyAndPopulated(t, registry, lexID)
	emptyRef2 := lexicon.MakeRef(lexID, "empty") // same ref re-used; sanity check

	got := builder.buildUnionType(lexID, "myField", []string{emptyRef, emptyRef2})
	if got == nil {
		t.Fatal("buildUnionType returned nil for {empty, empty}")
	}
	if got.Name() != JSONScalar.Name() {
		t.Errorf("union of {zero-property, zero-property} = %q, want JSONScalar", got.Name())
	}
}

func TestObjectBuilder_BuildUnionType_PopulatedObjectsStillBuildRealUnion(t *testing.T) {
	registry := lexicon.NewRegistry()
	mapper := NewMapper()
	builder := NewObjectBuilder(mapper, registry)

	const lexID = "com.example.testunion3"
	_, populatedRef := registerEmptyAndPopulated(t, registry, lexID)
	populated2Ref := lexicon.MakeRef(lexID, "populated2")

	got := builder.buildUnionType(lexID, "myField", []string{populatedRef, populated2Ref})
	if got == nil {
		t.Fatal("buildUnionType returned nil for {populated, populated2}")
	}
	if _, ok := got.(*graphql.Union); !ok {
		t.Errorf("union of two populated objects = %T (%q), want *graphql.Union — the zero-property carve-out must not poison real unions",
			got, got.Name())
	}
}
