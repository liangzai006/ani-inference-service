package resources

import "testing"

func TestAggregateDeploymentMultipliesResourceQuantitiesByReplicas(t *testing.T) {
	got, err := Aggregate(Spec{
		Requests: map[string]string{"cpu": "2", "memory": "8Gi", "nvidia.com/gpu": "1"},
		Limits:   map[string]string{"cpu": "4", "memory": "16Gi", "nvidia.com/gpu": "1"},
	}, 2, "deployment", 1)
	if err != nil {
		t.Fatalf("Aggregate() error = %v", err)
	}
	if got.Units != 2 {
		t.Fatalf("units = %d, want 2", got.Units)
	}
	if got.Resources.Requests["cpu"] != "4" || got.Resources.Requests["memory"] != "16Gi" || got.Resources.Requests["nvidia.com/gpu"] != "2" {
		t.Fatalf("requests = %#v", got.Resources.Requests)
	}
	if got.Resources.Limits["cpu"] != "8" || got.Resources.Limits["memory"] != "32Gi" || got.Resources.Limits["nvidia.com/gpu"] != "2" {
		t.Fatalf("limits = %#v", got.Resources.Limits)
	}
}

func TestAggregateLeaderWorkerSetIncludesLeaderAndWorkers(t *testing.T) {
	got, err := Aggregate(Spec{Requests: map[string]string{"cpu": "500m"}}, 2, "leader_worker_set", 3)
	if err != nil {
		t.Fatalf("Aggregate() error = %v", err)
	}
	if got.Units != 8 {
		t.Fatalf("units = %d, want 8", got.Units)
	}
	if got.Resources.Requests["cpu"] != "4" {
		t.Fatalf("requests = %#v, want cpu=4", got.Resources.Requests)
	}
}

func TestAggregateRejectsInvalidRuntimeShape(t *testing.T) {
	tests := []struct {
		name, mode        string
		replicas, workers int32
	}{
		{name: "zero replicas", mode: "deployment", replicas: 0, workers: 1},
		{name: "unknown mode", mode: "bogus", replicas: 1, workers: 1},
		{name: "zero workers", mode: "leader_worker_set", replicas: 1, workers: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Aggregate(Spec{}, tt.replicas, tt.mode, tt.workers); err == nil {
				t.Fatal("Aggregate() accepted invalid runtime shape")
			}
		})
	}
}
