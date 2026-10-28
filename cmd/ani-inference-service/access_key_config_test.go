package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfiguredAccessKeyCipher(t *testing.T) {
	t.Setenv("ANI_INFERENCE_ACCESS_KEY_ENCRYPTION_KEY_FILE", "")
	if _, err := configuredAccessKeyCipher(); err == nil {
		t.Fatal("missing key configuration must fail")
	}
	path := filepath.Join(t.TempDir(), "key")
	t.Setenv("ANI_INFERENCE_ACCESS_KEY_ENCRYPTION_KEY_FILE", path)
	if _, err := configuredAccessKeyCipher(); err == nil {
		t.Fatal("missing key file must fail")
	}
	for _, size := range []int{0, 16, 31, 33} {
		if err := os.WriteFile(path, make([]byte, size), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := configuredAccessKeyCipher(); err == nil {
			t.Fatalf("invalid key size %d must fail", size)
		}
	}
	if err := os.WriteFile(path, make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredAccessKeyCipher(); err != nil {
		t.Fatal(err)
	}
}
