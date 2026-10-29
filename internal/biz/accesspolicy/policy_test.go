package accesspolicy

import (
	"errors"
	"reflect"
	"testing"
)

func TestNormalizeAndModelAuthorization(t *testing.T) {
	p, err := (Policy{Name: " client ", AllowedModelNames: []string{" model-b ", "model-a", "model-b"}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "client" || !reflect.DeepEqual(p.AllowedModelNames, []string{"model-a", "model-b"}) {
		t.Fatalf("unexpected normalized policy: %+v", p)
	}
	if !p.AllowsModel("model-a") || p.AllowsModel("model-c") || (Policy{}).AllowsModel("model-a") {
		t.Fatal("model authorization must use exact matches and deny an empty list")
	}
}

func TestRejectInvalidPolicy(t *testing.T) {
	for _, p := range []Policy{
		{Name: " "},
		{Name: "client", AllowedModelNames: []string{" "}},
		{Name: "client", RequestsPerSecond: -1},
		{Name: "client", RequestsPerMinute: -1},
		{Name: "client", RequestsPerHour: -1},
	} {
		if _, err := p.Normalize(); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("policy %+v: got %v, want invalid input", p, err)
		}
	}
}
