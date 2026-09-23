package main

import "testing"

func TestReadyOnStartWithoutBackground(t *testing.T) {
	if !readyOnStartForBackground(nil) {
		t.Fatal("an inference control plane without a background runtime should be ready after startup")
	}
}
