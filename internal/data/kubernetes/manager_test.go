package kubernetes

import (
	"context"
	"testing"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

func managerOptionsForTest() manager.Options {
	// Other tests create the same controller in this process. This constructor
	// test never starts its manager; production keeps duplicate-name validation.
	skipNameValidation := true
	return manager.Options{Controller: config.Controller{SkipNameValidation: &skipNameValidation}}
}

func TestNewManagerRequiresExplicitRESTConfig(t *testing.T) {
	if _, err := NewManager(nil, &Controller{}, managerOptionsForTest()); err == nil {
		t.Fatal("NewManager accepted a nil REST config")
	}
}

func TestNewManagerRequiresController(t *testing.T) {
	if _, err := NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, nil, managerOptionsForTest()); err == nil {
		t.Fatal("NewManager accepted a nil controller")
	}
}

func TestNewManagerWiresControllerWithoutStartingIt(t *testing.T) {
	mgr, err := NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, &Controller{
		Work: managerTestNotifier{},
	}, managerOptionsForTest())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	if mgr == nil {
		t.Fatal("NewManager() returned nil manager")
	}
}

type managerTestNotifier struct{}

func (managerTestNotifier) Notify(context.Context, string, string, int64) error { return nil }
