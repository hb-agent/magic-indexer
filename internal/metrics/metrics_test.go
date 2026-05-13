package metrics

import (
	"testing"
)

// counterValue reads the current value of a single labelled
// counter out of the package registry. Equivalent to the
// counterDelta helper in internal/notifications/extractors/shared_test.go;
// duplicated here to keep test packages independent.
func counterValue(t *testing.T, name, labelName, labelValue string) float64 {
	t.Helper()
	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("registry gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == labelName && l.GetValue() == labelValue {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

func TestGraphQLQueryTimeout_IncrementsLabeledCounter(t *testing.T) {
	const name = "hypergoat_graphql_query_timeout_total"
	const label = "route"
	const value = "public"

	before := counterValue(t, name, label, value)
	GraphQLQueryTimeout(value)
	GraphQLQueryTimeout(value)
	after := counterValue(t, name, label, value)

	if got, want := after-before, float64(2); got != want {
		t.Errorf("counter delta = %v, want %v", got, want)
	}
}

func TestGraphQLQueryTimeout_RegisteredOnPackageRegistry(t *testing.T) {
	const name = "hypergoat_graphql_query_timeout_total"
	families, err := Registry.Gather()
	if err != nil {
		t.Fatalf("registry gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() == name {
			return
		}
	}
	t.Errorf("expected counter %s registered on package registry", name)
}
