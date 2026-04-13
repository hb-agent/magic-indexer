// Record validation against lexicon schemas.
//
// Two functions serve different roles:
//
//   - Validate() checks a raw record for ingestion-time validity (read-only).
//     Used by the Jetstream consumer and backfiller to log or reject bad records.
//
//   - SanitizeRecord() transforms a deserialized record map for GraphQL output.
//     Truncates over-long strings, nulls invalid optional fields, and returns
//     nil when required fields are missing (causing the record to be skipped).
//     Always on at query time — it is projection for GraphQL type safety.
package lexicon

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// Validator checks records against their lexicon definitions.
type Validator struct {
	registry *Registry
}

// NewValidator creates a Validator backed by the given Registry.
func NewValidator(r *Registry) *Validator {
	return &Validator{registry: r}
}

// ValidationResult holds the outcome of a Validate() call.
type ValidationResult struct {
	Valid      bool
	Violations []Violation
}

// Violation describes a single validation issue.
type Violation struct {
	Field   string // dot-path, e.g. "contributors.0.contributorIdentity"
	Rule    string // "required", "type", "format", "enum", "$type"
	Message string
}

// Validate checks a raw JSON record against its lexicon definition.
// It does NOT modify the record — it is a read-only inspection.
// Unknown collections pass through as valid.
func (v *Validator) Validate(collection string, record json.RawMessage) *ValidationResult {
	def, ok := v.registry.GetRecordDef(collection)
	if !ok {
		return &ValidationResult{Valid: true}
	}

	var data map[string]interface{}
	if err := json.Unmarshal(record, &data); err != nil {
		return &ValidationResult{
			Valid:      false,
			Violations: []Violation{{Rule: "json", Message: "invalid JSON: " + err.Error()}},
		}
	}

	var violations []Violation

	// Check $type matches collection if present.
	if t, ok := data["$type"].(string); ok && t != collection {
		violations = append(violations, Violation{
			Field:   "$type",
			Rule:    "$type",
			Message: fmt.Sprintf("$type %q does not match collection %q", t, collection),
		})
		return &ValidationResult{Valid: false, Violations: violations}
	}

	// Validate properties.
	violations = validateProperties(def.Properties, data, "")
	valid := true
	for _, v := range violations {
		if v.Rule == "required" || v.Rule == "type" || v.Rule == "format" || v.Rule == "enum" {
			// Check if the field is required — only required-field violations are fatal.
			if isRequiredViolation(v) {
				valid = false
			}
		}
	}

	return &ValidationResult{Valid: valid, Violations: violations}
}

func isRequiredViolation(v Violation) bool {
	return strings.HasPrefix(v.Message, "[required]")
}

// validateProperties checks each property entry against the data map.
func validateProperties(props []PropertyEntry, data map[string]interface{}, prefix string) []Violation {
	var violations []Violation

	for _, entry := range props {
		field := entry.Name
		prop := &entry.Property
		path := field
		if prefix != "" {
			path = prefix + "." + field
		}

		val, exists := data[field]

		// Required field checks.
		if prop.Required {
			if !exists || val == nil {
				violations = append(violations, Violation{
					Field:   path,
					Rule:    "required",
					Message: "[required] field is missing or null",
				})
				continue
			}
			if !checkJSONType(val, prop.Type) {
				violations = append(violations, Violation{
					Field:   path,
					Rule:    "type",
					Message: fmt.Sprintf("[required] expected type %q, got %T", prop.Type, val),
				})
				continue
			}
			if s, ok := val.(string); ok {
				if prop.Format != "" && !validateFormat(s, prop.Format) {
					violations = append(violations, Violation{
						Field:   path,
						Rule:    "format",
						Message: fmt.Sprintf("[required] invalid %s format", prop.Format),
					})
					continue
				}
				if len(prop.Enum) > 0 && !inEnum(s, prop.Enum) {
					violations = append(violations, Violation{
						Field:   path,
						Rule:    "enum",
						Message: fmt.Sprintf("[required] value %q not in enum %v", s, prop.Enum),
					})
					continue
				}
			}
			// maxLength/maxGraphemes on required fields are warnings, not rejections.
			if s, ok := val.(string); ok {
				if prop.MaxLength != nil && len(s) > *prop.MaxLength {
					violations = append(violations, Violation{
						Field:   path,
						Rule:    "maxLength",
						Message: fmt.Sprintf("string length %d exceeds maxLength %d", len(s), *prop.MaxLength),
					})
				}
				if prop.MaxGraphemes != nil && utf8.RuneCountInString(s) > *prop.MaxGraphemes {
					violations = append(violations, Violation{
						Field:   path,
						Rule:    "maxGraphemes",
						Message: fmt.Sprintf("grapheme count exceeds maxGraphemes %d", *prop.MaxGraphemes),
					})
				}
			}
			continue
		}

		// Optional field checks (field exists and is non-nil).
		if !exists || val == nil {
			continue
		}

		if !checkJSONType(val, prop.Type) {
			violations = append(violations, Violation{
				Field:   path,
				Rule:    "type",
				Message: fmt.Sprintf("expected type %q, got %T", prop.Type, val),
			})
		}
		if s, ok := val.(string); ok {
			if prop.Format != "" && !validateFormat(s, prop.Format) {
				violations = append(violations, Violation{
					Field:   path,
					Rule:    "format",
					Message: fmt.Sprintf("invalid %s format", prop.Format),
				})
			}
			if len(prop.Enum) > 0 && !inEnum(s, prop.Enum) {
				violations = append(violations, Violation{
					Field:   path,
					Rule:    "enum",
					Message: fmt.Sprintf("value %q not in enum %v", s, prop.Enum),
				})
			}
		}
	}

	return violations
}

// SanitizeRecord transforms a deserialized record map for GraphQL output.
// Returns nil if the record should be skipped (missing required fields).
// The caller should look up *RecordDef once per collection, not per record.
// If def is nil (unknown collection), data is returned unchanged.
func SanitizeRecord(def *RecordDef, registry *Registry, data map[string]interface{}) map[string]interface{} {
	if def == nil {
		return data
	}
	return sanitizeObject(def.Properties, registry, data, "", 0)
}

const maxSanitizeDepth = 32

// sanitizeObject applies sanitization rules to an object's properties.
// Returns nil if any required field is missing/invalid (record should be skipped).
func sanitizeObject(props []PropertyEntry, registry *Registry, data map[string]interface{}, lexiconCtx string, depth int) map[string]interface{} {
	if depth > maxSanitizeDepth {
		return data
	}

	for _, entry := range props {
		field := entry.Name
		prop := &entry.Property
		val, exists := data[field]

		// Required field: must be present, non-null, correct type.
		if prop.Required {
			if !exists || val == nil {
				return nil
			}
			if !checkJSONType(val, prop.Type) {
				return nil
			}
			if s, ok := val.(string); ok {
				if prop.Format != "" && !validateFormat(s, prop.Format) {
					return nil
				}
				if len(prop.Enum) > 0 && !inEnum(s, prop.Enum) {
					return nil
				}
				// Truncate over-long required strings (keep the record).
				data[field] = truncateString(s, prop.MaxLength, prop.MaxGraphemes)
			}
			continue
		}

		// Optional field: sanitize or null out.
		if !exists || val == nil {
			continue
		}

		switch {
		case prop.Type == TypeString || prop.Type == "":
			s, ok := val.(string)
			if !ok {
				data[field] = nil
				continue
			}
			if prop.Format != "" && !validateFormat(s, prop.Format) {
				data[field] = nil
				continue
			}
			if len(prop.Enum) > 0 && !inEnum(s, prop.Enum) {
				data[field] = nil
				continue
			}
			data[field] = truncateString(s, prop.MaxLength, prop.MaxGraphemes)

		case prop.Type == TypeArray:
			arr, ok := val.([]interface{})
			if !ok {
				data[field] = nil
				continue
			}
			if prop.MaxLength != nil && len(arr) > *prop.MaxLength {
				arr = arr[:*prop.MaxLength]
				data[field] = arr
			}
			// Sanitize array items if they reference an object type.
			if prop.Items != nil && registry != nil {
				sanitizeArrayItems(prop.Items, registry, arr, lexiconCtx, depth)
			}

		case prop.Type == TypeRef:
			if obj, ok := val.(map[string]interface{}); ok && registry != nil {
				sanitizeRefObject(prop.Ref, registry, obj, lexiconCtx, depth)
			}

		case prop.Type == TypeUnion:
			if obj, ok := val.(map[string]interface{}); ok && registry != nil {
				sanitizeUnionObject(prop.Refs, registry, obj, lexiconCtx, depth)
			}

		default:
			if !checkJSONType(val, prop.Type) {
				data[field] = nil
			}
		}
	}

	return data
}

// sanitizeArrayItems sanitizes each item in an array based on the item type definition.
func sanitizeArrayItems(items *ArrayItems, registry *Registry, arr []interface{}, lexiconCtx string, depth int) {
	if items.Type != TypeRef || items.Ref == "" {
		return
	}
	resolved, found := registry.ResolveRef(items.Ref, lexiconCtx)
	if !found {
		return
	}
	objDef, ok := resolveToObjectDef(resolved)
	if !ok {
		return
	}
	for i, item := range arr {
		if obj, ok := item.(map[string]interface{}); ok {
			result := sanitizeObject(objDef.Properties, registry, obj, lexiconCtx, depth+1)
			if result == nil {
				arr[i] = nil // null out invalid nested objects
			}
		}
	}
}

// sanitizeRefObject sanitizes a single ref-typed field.
func sanitizeRefObject(ref string, registry *Registry, obj map[string]interface{}, lexiconCtx string, depth int) {
	resolved, found := registry.ResolveRef(ref, lexiconCtx)
	if !found {
		return
	}
	objDef, ok := resolveToObjectDef(resolved)
	if !ok {
		return
	}
	sanitizeObject(objDef.Properties, registry, obj, lexiconCtx, depth+1)
}

// sanitizeUnionObject sanitizes a union-typed field by resolving the $type discriminator.
func sanitizeUnionObject(refs []string, registry *Registry, obj map[string]interface{}, lexiconCtx string, depth int) {
	typeName, _ := obj["$type"].(string)
	if typeName == "" {
		return // can't determine union branch without $type
	}
	for _, ref := range refs {
		resolved, found := registry.ResolveRef(ref, lexiconCtx)
		if !found {
			continue
		}
		objDef, ok := resolveToObjectDef(resolved)
		if !ok {
			continue
		}
		// Match by checking if the ref's NSID matches the $type.
		if refMatchesType(ref, typeName, lexiconCtx) {
			sanitizeObject(objDef.Properties, registry, obj, lexiconCtx, depth+1)
			return
		}
	}
}

// resolveToObjectDef extracts an *ObjectDef from a resolved ref.
func resolveToObjectDef(resolved interface{}) (*ObjectDef, bool) {
	switch d := resolved.(type) {
	case *ObjectDef:
		return d, true
	case *RecordDef:
		// Treat record defs as object-like for property validation.
		return &ObjectDef{
			Type:       d.Type,
			Properties: d.Properties,
		}, true
	default:
		return nil, false
	}
}

// refMatchesType checks if a ref identifier matches a $type value.
func refMatchesType(ref, typeName, lexiconCtx string) bool {
	// Expand local refs: "#foo" → "lexiconCtx#foo"
	if strings.HasPrefix(ref, "#") && lexiconCtx != "" {
		ref = lexiconCtx + ref
	}
	return ref == typeName
}

// --- Helpers ---

// checkJSONType checks if a Go value from JSON unmarshal matches the expected lexicon type.
func checkJSONType(val interface{}, expectedType string) bool {
	switch expectedType {
	case TypeString:
		_, ok := val.(string)
		return ok
	case TypeInteger:
		f, ok := val.(float64)
		return ok && f == float64(int64(f))
	case TypeBoolean:
		_, ok := val.(bool)
		return ok
	case TypeArray:
		_, ok := val.([]interface{})
		return ok
	case TypeRef, TypeUnion:
		// Refs and unions are typically objects in JSON.
		_, ok := val.(map[string]interface{})
		return ok
	case TypeBlob:
		_, ok := val.(map[string]interface{})
		return ok
	case TypeCIDLink:
		// CID links are represented as objects with a $link field.
		if m, ok := val.(map[string]interface{}); ok {
			_, has := m["$link"]
			return has
		}
		return false
	case TypeBytes:
		// Bytes are base64-encoded strings.
		_, ok := val.(string)
		return ok
	case TypeUnknown, TypeObject, "":
		return true // accept anything
	default:
		return true // unknown types pass through
	}
}

// validateFormat checks if a string value conforms to the declared format.
func validateFormat(value, format string) bool {
	switch format {
	case FormatDatetime:
		_, err := time.Parse(time.RFC3339, value)
		if err != nil {
			// Try without timezone (some records use ISO 8601 without offset).
			_, err = time.Parse("2006-01-02T15:04:05", value)
		}
		return err == nil
	case FormatURI:
		u, err := url.Parse(value)
		return err == nil && u.Scheme != ""
	case FormatATURI:
		return strings.HasPrefix(value, "at://")
	case FormatDID:
		return strings.HasPrefix(value, "did:")
	case FormatHandle:
		return len(value) > 0 && strings.Contains(value, ".")
	case FormatCID, FormatNSID, FormatLanguage, FormatRecordKey, FormatTID:
		return len(value) > 0 // basic non-empty check for less common formats
	default:
		return true // unknown formats pass
	}
}

// truncateString truncates a string by maxLength (bytes) and maxGraphemes (runes).
// Returns the original string if no truncation is needed.
func truncateString(s string, maxLength, maxGraphemes *int) string {
	if maxLength != nil && len(s) > *maxLength {
		s = truncateUTF8(s, *maxLength)
	}
	if maxGraphemes != nil && utf8.RuneCountInString(s) > *maxGraphemes {
		s = truncateRunes(s, *maxGraphemes)
	}
	return s
}

// truncateUTF8 truncates a string to at most maxBytes bytes without splitting
// a multi-byte UTF-8 sequence.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	// Walk back from maxBytes to find a valid rune boundary.
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// truncateRunes truncates a string to at most maxRunes runes.
// This is a conservative approximation of grapheme cluster count
// (rune count >= grapheme count, so we may truncate slightly less
// than a true grapheme-aware implementation would allow).
func truncateRunes(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
}

// inEnum checks if a value is in the allowed enum list.
func inEnum(val string, enum []string) bool {
	for _, e := range enum {
		if val == e {
			return true
		}
	}
	return false
}
