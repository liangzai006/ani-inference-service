package contractv1

import (
	"bytes"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

func TestCatalogCommandCanonicalAndSelection(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{ContractVersion: 2, Name: "catalog", ModelVersionId: "11111111-1111-4111-8111-111111111111", Replicas: 1, Engine: &inferencev1.EngineSpec{Type: "vllm", Command: "exec serve /models", ImageId: "22222222-2222-4222-8222-222222222222", ImageScope: "platform"}}
	if err := ValidateBusinessPayload(req); err != nil {
		t.Fatal(err)
	}
	raw, err := CanonicalBusinessPayload(req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBusinessPayload(raw)
	if err != nil || decoded.GetContractVersion() != 2 || decoded.GetEngine().GetImageId() != req.Engine.ImageId {
		t.Fatalf("roundtrip: %v", err)
	}
	req.Engine.ImageScope = "tenant"
	changed, err := CanonicalBusinessPayload(req)
	if err != nil || bytes.Equal(raw, changed) {
		t.Fatalf("selection tampering not bound: %v", err)
	}
	req.Engine.Image = "registry/unsafe:latest"
	if ValidateBusinessPayload(req) == nil {
		t.Fatal("accepted raw image override")
	}
	req.Engine.Image = ""
	req.ContractVersion = 0
	if ValidateBusinessPayload(req) == nil {
		t.Fatal("legacy command accepted catalog fields")
	}
	if _, err := CanonicalBusinessPayload(req); err == nil {
		t.Fatal("legacy digest ignored populated catalog fields")
	}
}
