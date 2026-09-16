package admission

import (
	"fmt"

	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/resources"
)

type ResourceInput struct{ Requests, Limits map[string]string }
type CreateRequest struct {
	Replicas, WorkerReplicas int32
	RuntimeMode              string
	Resources                ResourceInput
}

func ValidateCreate(in CreateRequest) error {
	mode := in.RuntimeMode
	if mode == "" || mode == "deployment" {
		if in.Replicas != 1 {
			return fmt.Errorf("replicas must be 1 for deployment runtime")
		}
		if in.WorkerReplicas > 1 {
			return fmt.Errorf("worker_replicas is only valid for leader_worker_set runtime")
		}
	} else if mode == "leader_worker_set" {
		if in.Replicas < 1 || in.WorkerReplicas < 2 {
			return fmt.Errorf("leader_worker_set requires replicas >= 1 and worker_replicas >= 2")
		}
	} else {
		return fmt.Errorf("unsupported runtime mode %q", mode)
	}
	_, err := resources.Normalize(resources.Spec{Requests: in.Resources.Requests, Limits: in.Resources.Limits})
	return err
}
