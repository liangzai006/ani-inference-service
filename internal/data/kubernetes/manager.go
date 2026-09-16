package kubernetes

import (
	"errors"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// NewManager constructs and wires the controller-runtime manager for this
// service. A REST config is required explicitly: callers must not silently
// fall back to a fake client or start an operator without Kubernetes access.
// The returned manager is not started by this function.
func NewManager(config *rest.Config, controller *Controller, options manager.Options) (manager.Manager, error) {
	if config == nil {
		return nil, errors.New("kubernetes REST config is required")
	}
	if controller == nil {
		return nil, errors.New("inference controller is required")
	}
	if options.Scheme == nil {
		options.Scheme = runtime.NewScheme()
	}
	// The Kratos admin server owns this process's health and metrics endpoints.
	// Disable controller-runtime's listeners unless the composition explicitly
	// opts in, avoiding a hidden :8080 bind when the operator is embedded.
	if options.Metrics.BindAddress == "" {
		options.Metrics.BindAddress = "0"
	}
	if options.HealthProbeBindAddress == "" {
		options.HealthProbeBindAddress = "0"
	}
	if options.PprofBindAddress == "" {
		options.PprofBindAddress = "0"
	}
	if err := AddToScheme(options.Scheme); err != nil {
		return nil, err
	}
	mgr, err := manager.New(config, options)
	if err != nil {
		return nil, err
	}
	if err := controller.SetupWithManager(mgr); err != nil {
		return nil, err
	}
	return mgr, nil
}
