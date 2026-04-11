package schema

import (
	"testing"
)

func TestParseLabelFilter_UsesDefault(t *testing.T) {
	args := map[string]interface{}{
		"labels": []interface{}{"high-quality"},
	}
	filter, err := parseLabelFilter(args, "did:plc:default")
	if err != nil {
		t.Fatalf("parseLabelFilter() error = %v", err)
	}
	if filter.LabelerSrc != "did:plc:default" {
		t.Errorf("LabelerSrc = %q, want did:plc:default", filter.LabelerSrc)
	}
	if len(filter.Include) != 1 || filter.Include[0] != "high-quality" {
		t.Errorf("Include = %v, want [high-quality]", filter.Include)
	}
}

func TestParseLabelFilter_ExplicitLabelerOverride(t *testing.T) {
	args := map[string]interface{}{
		"labels":     []interface{}{"standard"},
		"labelerDid": "did:plc:override",
	}
	filter, err := parseLabelFilter(args, "did:plc:default")
	if err != nil {
		t.Fatalf("parseLabelFilter() error = %v", err)
	}
	if filter.LabelerSrc != "did:plc:override" {
		t.Errorf("LabelerSrc = %q, want did:plc:override", filter.LabelerSrc)
	}
}

func TestParseLabelFilter_ErrorsWhenNoLabeler(t *testing.T) {
	args := map[string]interface{}{
		"labels": []interface{}{"high-quality"},
	}
	_, err := parseLabelFilter(args, "")
	if err == nil {
		t.Fatal("expected error when filter is set and no labeler configured")
	}
}

func TestParseLabelFilter_NoFilterNoErrorWhenEmpty(t *testing.T) {
	args := map[string]interface{}{}
	_, err := parseLabelFilter(args, "")
	if err != nil {
		t.Errorf("unexpected error when filter is empty and no labeler: %v", err)
	}
}

func TestParseLabelFilter_TruncatesOversizedLists(t *testing.T) {
	raw := make([]interface{}, MaxLabelFilterValues+10)
	for i := range raw {
		raw[i] = "val"
	}
	args := map[string]interface{}{
		"labels": raw,
	}
	filter, err := parseLabelFilter(args, "did:plc:x")
	if err != nil {
		t.Fatalf("parseLabelFilter() error = %v", err)
	}
	if len(filter.Include) != MaxLabelFilterValues {
		t.Errorf("Include length = %d, want %d (truncated)",
			len(filter.Include), MaxLabelFilterValues)
	}
}

func TestParseLabelFilter_CombinesIncludeAndExclude(t *testing.T) {
	args := map[string]interface{}{
		"labels":        []interface{}{"high-quality"},
		"excludeLabels": []interface{}{"draft", "likely-test"},
	}
	filter, err := parseLabelFilter(args, "did:plc:x")
	if err != nil {
		t.Fatalf("parseLabelFilter() error = %v", err)
	}
	if len(filter.Include) != 1 || len(filter.Exclude) != 2 {
		t.Errorf("Include=%v, Exclude=%v", filter.Include, filter.Exclude)
	}
}
