package main

import "testing"

func TestConfiguredModelMaterializerSettingsRequireFetcherImage(t *testing.T) {
	t.Setenv("ANI_MODEL_MATERIALIZER_IMAGE", "")
	t.Setenv("ANI_MODEL_STORAGE_CLASS", "")

	if _, err := configuredModelMaterializerSettings(); err == nil {
		t.Fatal("configuredModelMaterializerSettings() error = nil, want missing image error")
	}
}

func TestConfiguredModelMaterializerSettingsDefaultStorageClass(t *testing.T) {
	t.Setenv("ANI_MODEL_MATERIALIZER_IMAGE", "registry.example/fetcher@sha256:fixed")
	t.Setenv("ANI_MODEL_STORAGE_CLASS", "")

	got, err := configuredModelMaterializerSettings()
	if err != nil {
		t.Fatalf("configuredModelMaterializerSettings() error = %v", err)
	}
	if got.Image != "registry.example/fetcher@sha256:fixed" || got.StorageClass != "cephfs" {
		t.Fatalf("settings = %#v, want pinned image and cephfs", got)
	}
}
