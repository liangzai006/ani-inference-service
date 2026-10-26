package contractv1

import (
	"bytes"
	"testing"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
)

func TestCatalogCommandCanonicalAndSelection(t *testing.T) {
	req := &inferencev1.CreateInferenceServiceRequest{ContractVersion: 2, Name: "catalog", ModelVersionId: "11111111-1111-4111-8111-111111111111", Replicas: 1, Engine: &inferencev1.EngineSpec{Type: "vllm", Command: "exec serve /models", ImageId: "img_23d024b1b5e343cb82b38bae83196ccd", ImageScope: "platform"}}
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

func TestCatalogImageIDMatchesResourceContract(t *testing.T) {
	engine := &inferencev1.EngineSpec{Type: "vllm", Command: "serve", ImageScope: "tenant", ImageId: "img_23d024b1b5e343cb82b38bae83196ccd"}
	if err := ValidateEngineSelection(engine, 2); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"22222222-2222-4222-8222-222222222222", "img_ABCDEF", "img_23d024b1b5e343cb82b38bae83196ccd/extra"} {
		engine.ImageId = invalid
		if ValidateEngineSelection(engine, 2) == nil {
			t.Fatalf("invalid catalog ID accepted: %q", invalid)
		}
	}
}
