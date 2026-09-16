package postgres

import (
	"context"
	"testing"

	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
	kube "github.com/zhangzhe-ctrl/ani-inference-service/internal/data/kubernetes"
)

type admissionRuntimeSource struct{ runtime kube.DesiredRuntime }

func (s admissionRuntimeSource) CurrentRuntime(context.Context, string, string, int64) (kube.DesiredRuntime, error) {
	return s.runtime, nil
}

func TestAdmissionValidatesPersistedRuntimeShape(t *testing.T) {
	a := &Admission{Source: admissionRuntimeSource{runtime: kube.DesiredRuntime{RuntimeSpec: kube.RuntimeSpec{
		Replicas: 1, RuntimeMode: "deployment", Resources: resources.Normalized{Requests: map[string]string{"cpu": "2"}},
	}}}}
	if err := a.Admit(context.Background(), inferencebiz.OperationContext{TenantID: "tenant", ServiceID: "service", TargetGeneration: 1}); err != nil {
		t.Fatal(err)
	}
	a.Source = admissionRuntimeSource{runtime: kube.DesiredRuntime{RuntimeSpec: kube.RuntimeSpec{Replicas: 2, RuntimeMode: "deployment"}}}
	if err := a.Admit(context.Background(), inferencebiz.OperationContext{TenantID: "tenant", ServiceID: "service", TargetGeneration: 1}); err == nil {
		t.Fatal("invalid deployment replica shape was accepted")
	}
}
