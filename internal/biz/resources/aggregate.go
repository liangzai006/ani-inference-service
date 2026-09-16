package resources

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Demand is the aggregate resource requirement for the runtime's inference
// containers. LWS uses the same resource spec for its leader and each worker.
type Demand struct {
	Resources Spec
	Units     int64
}

func Aggregate(spec Spec, replicas int32, runtimeMode string, workerReplicas int32) (Demand, error) {
	if replicas < 1 {
		return Demand{}, fmt.Errorf("replicas must be positive")
	}
	units := int64(replicas)
	switch runtimeMode {
	case "", "deployment":
		if workerReplicas > 1 || workerReplicas < 0 {
			return Demand{}, fmt.Errorf("worker_replicas is only valid for leader_worker_set runtime")
		}
	case "leader_worker_set":
		if workerReplicas < 2 {
			return Demand{}, fmt.Errorf("leader_worker_set requires at least two workers")
		}
		units *= int64(workerReplicas) + 1
	default:
		return Demand{}, fmt.Errorf("unsupported runtime mode %q", runtimeMode)
	}
	values, err := Normalize(spec)
	if err != nil {
		return Demand{}, err
	}
	for _, quantities := range []map[string]string{values.Requests, values.Limits} {
		for name, value := range quantities {
			quantity, err := resource.ParseQuantity(value)
			if err != nil {
				return Demand{}, err
			}
			if !quantity.Mul(units) {
				return Demand{}, fmt.Errorf("aggregate resource %s exceeds quantity precision", name)
			}
			quantities[name] = quantity.String()
		}
	}
	return Demand{Resources: Spec{Requests: values.Requests, Limits: values.Limits}, Units: units}, nil
}
