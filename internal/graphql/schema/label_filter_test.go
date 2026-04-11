package schema

import (
	"testing"
)

func TestParseLabelFilter_NeutralDefault(t *testing.T) {
	args := map[string]interface{}{
		"labels": []interface{}{"high-quality"},
	}
	filter := parseLabelFilter(args)
	if len(filter.LabelerSrcs) != 0 {
		t.Errorf("expected empty LabelerSrcs (neutral default), got %v", filter.LabelerSrcs)
	}
	if len(filter.Include) != 1 || filter.Include[0] != "high-quality" {
		t.Errorf("Include = %v, want [high-quality]", filter.Include)
	}
}

func TestParseLabelFilter_LabelerDidsList(t *testing.T) {
	args := map[string]interface{}{
		"labels":      []interface{}{"standard"},
		"labelerDids": []interface{}{"did:plc:a", "did:plc:b"},
	}
	filter := parseLabelFilter(args)
	if len(filter.LabelerSrcs) != 2 {
		t.Fatalf("expected 2 LabelerSrcs, got %v", filter.LabelerSrcs)
	}
	if filter.LabelerSrcs[0] != "did:plc:a" || filter.LabelerSrcs[1] != "did:plc:b" {
		t.Errorf("LabelerSrcs = %v", filter.LabelerSrcs)
	}
}

func TestParseLabelFilter_EmptyArgsIsNoFilter(t *testing.T) {
	args := map[string]interface{}{}
	filter := parseLabelFilter(args)
	if !filter.IsEmpty() {
		t.Errorf("expected empty filter, got %+v", filter)
	}
	if len(filter.LabelerSrcs) != 0 {
		t.Errorf("expected no labeler srcs, got %v", filter.LabelerSrcs)
	}
}

func TestParseLabelFilter_TruncatesOversizedValues(t *testing.T) {
	raw := make([]interface{}, MaxLabelFilterValues+10)
	for i := range raw {
		raw[i] = "val"
	}
	args := map[string]interface{}{
		"labels": raw,
	}
	filter := parseLabelFilter(args)
	if len(filter.Include) != MaxLabelFilterValues {
		t.Errorf("Include length = %d, want %d (truncated)",
			len(filter.Include), MaxLabelFilterValues)
	}
}

func TestParseLabelFilter_TruncatesOversizedLabelerDids(t *testing.T) {
	raw := make([]interface{}, MaxLabelFilterLabelers+5)
	for i := range raw {
		raw[i] = "did:plc:x"
	}
	args := map[string]interface{}{
		"labelerDids": raw,
		"labels":      []interface{}{"spam"},
	}
	filter := parseLabelFilter(args)
	if len(filter.LabelerSrcs) != MaxLabelFilterLabelers {
		t.Errorf("LabelerSrcs length = %d, want %d (truncated)",
			len(filter.LabelerSrcs), MaxLabelFilterLabelers)
	}
}

func TestParseLabelFilter_CombinesIncludeAndExclude(t *testing.T) {
	args := map[string]interface{}{
		"labels":        []interface{}{"high-quality"},
		"excludeLabels": []interface{}{"draft", "likely-test"},
		"labelerDids":   []interface{}{"did:plc:x"},
	}
	filter := parseLabelFilter(args)
	if len(filter.Include) != 1 || len(filter.Exclude) != 2 {
		t.Errorf("Include=%v, Exclude=%v", filter.Include, filter.Exclude)
	}
}
