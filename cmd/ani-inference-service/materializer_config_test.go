package main

import "testing"

func TestConfiguredModelMaterializerSettingsRequireFetcherImage(t *testing.T) {
	t.Setenv("ANI_MODEL_MATERIALIZER_IMAGE", "")
	t.Setenv("ANI_MODEL_STORAGE_CLASS", "")
	t.Setenv("ANI_KUBERNETES_SERVICE_ACCOUNT", "")

	if _, err := configuredModelMaterializerSettings(); err == nil {
		t.Fatal("configuredModelMaterializerSettings() error = nil, want missing image error")
	}
}

func TestConfiguredModelMaterializerSettingsUsesClusterDefaultStorageClass(t *testing.T) {
	t.Setenv("ANI_MODEL_MATERIALIZER_IMAGE", "registry.example/fetcher@sha256:fixed")
	t.Setenv("ANI_MODEL_STORAGE_CLASS", "")
	t.Setenv("ANI_KUBERNETES_SERVICE_ACCOUNT", "ani-inference-runtime")

	got, err := configuredModelMaterializerSettings()
	if err != nil {
		t.Fatalf("configuredModelMaterializerSettings() error = %v", err)
	}
	if got.Image != "registry.example/fetcher@sha256:fixed" || got.StorageClass != "" || got.ServiceAccountName != "ani-inference-runtime" {
		t.Fatalf("settings = %#v, want image, empty storage class, and service account", got)
	}
}
