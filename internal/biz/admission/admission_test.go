package admission

import "testing"

func TestDeploymentAcceptsMultipleReplicas(t *testing.T) {
	if err := ValidateCreate(CreateRequest{Replicas: 2, Resources: ResourceInput{Requests: map[string]string{"cpu": "1"}}}); err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestLeaderWorkerSetAcceptsDistributedShape(t *testing.T) {
	if err := ValidateCreate(CreateRequest{RuntimeMode: "leader_worker_set", Replicas: 2, WorkerReplicas: 4}); err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestLeaderWorkerSetAcceptsSingleWorker(t *testing.T) {
	if err := ValidateCreate(CreateRequest{RuntimeMode: "leader_worker_set", Replicas: 1, WorkerReplicas: 1}); err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}

func TestCreateAcceptsKubernetesResourceMaps(t *testing.T) {
	err := ValidateCreate(CreateRequest{Replicas: 1, Resources: ResourceInput{
		Requests: map[string]string{"cpu": "2", "memory": "8Gi", "nvidia.com/gpu": "1"},
		Limits:   map[string]string{"cpu": "4", "memory": "16Gi", "nvidia.com/gpu": "1"},
	}})
	if err != nil {
		t.Fatalf("ValidateCreate() error = %v", err)
	}
}
