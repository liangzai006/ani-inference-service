package inference

import (
	"context"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/publication"
	"testing"
)

type resolvedPublication struct{ runnerPublication }

func (*resolvedPublication) Endpoint(context.Context, publication.Publication) (string, error) {
	return "http://node:30001", nil
}
func TestPublishPersistsAuthorityResolvedEndpoint(t *testing.T) {
	store := &runnerStore{op: operation(string(StepPublish))}
	provider := &resolvedPublication{runnerPublication: runnerPublication{publishConfirmed: true}}
	_, err := (&Runner{Store: store, Publication: provider, Audit: &runnerAudit{}}).Execute(context.Background(), item())
	if err != nil {
		t.Fatal(err)
	}
	if len(store.publication) != 2 || store.publication[1].URL != "http://node:30001" {
		t.Fatalf("endpoint not persisted: %+v", store.publication)
	}
}
