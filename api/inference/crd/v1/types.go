// Package v1 contains the typed Kubernetes API for InferenceService.
package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var GroupVersion = schema.GroupVersion{Group: "ani.kubercloud.com", Version: "v1"}

var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &InferenceService{}, &InferenceServiceList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

type InferenceServiceSpec struct {
	Generation      int64                       `json:"generation"`
	DesiredState    string                      `json:"desiredState"`
	ModelVersionID  string                      `json:"modelVersionId"`
	ModelRef        *ModelReference             `json:"modelRef,omitempty"`
	ServedModelName string                      `json:"servedModelName,omitempty"`
	Engine          *EngineSpec                 `json:"engine,omitempty"`
	Resource        corev1.ResourceRequirements `json:"resource,omitempty"`
	Replicas        int32                       `json:"replicas"`
	RuntimeMode     string                      `json:"runtimeMode,omitempty"`
	WorkerReplicas  int32                       `json:"workerReplicas,omitempty"`
	Endpoint        *EndpointSpec               `json:"endpoint,omitempty"`
}

type EndpointSpec struct {
	ContainerPort int32  `json:"containerPort"`
	ServicePort   int32  `json:"servicePort"`
	TargetPort    string `json:"targetPort"`
	Protocol      string `json:"protocol,omitempty"`
}

type ModelReference struct {
	ModelID        string `json:"modelId,omitempty"`
	VersionID      string `json:"versionId"`
	ArtifactDigest string `json:"artifactDigest,omitempty"`
}

type EngineSpec struct {
	Type    string   `json:"type,omitempty"`
	Image   string   `json:"image,omitempty"`
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type InferenceServiceStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	ObservedAt         *metav1.Time       `json:"observedAt,omitempty"`
	RuntimePhase       string             `json:"runtimePhase,omitempty"`
	ReadyReplicas      int32              `json:"readyReplicas,omitempty"`
	ModelReady         bool               `json:"modelReady,omitempty"`
	PublicationPhase   string             `json:"publicationPhase,omitempty"`
	InvocationHealth   string             `json:"invocationHealth,omitempty"`
	Runtime            *RuntimeStatus     `json:"runtime,omitempty"`
	Publication        *PublicationStatus `json:"publication,omitempty"`
	Invocation         *InvocationStatus  `json:"invocation,omitempty"`
	Operation          *OperationStatus   `json:"operation,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
}

type RuntimeStatus struct {
	Phase         string       `json:"phase,omitempty"`
	ReadyReplicas int32        `json:"readyReplicas,omitempty"`
	ModelReady    bool         `json:"modelReady,omitempty"`
	ObservedAt    *metav1.Time `json:"observedAt,omitempty"`
	StaleAfter    *metav1.Time `json:"staleAfter,omitempty"`
}
type PublicationStatus struct {
	Phase      string       `json:"phase,omitempty"`
	Effective  bool         `json:"effective,omitempty"`
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}
type InvocationStatus struct {
	Health     string       `json:"health,omitempty"`
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`
}
type OperationStatus struct {
	ID      string `json:"id,omitempty"`
	Kind    string `json:"kind,omitempty"`
	Phase   string `json:"phase,omitempty"`
	Step    string `json:"step,omitempty"`
	Message string `json:"message,omitempty"`
}

type InferenceService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              InferenceServiceSpec   `json:"spec,omitempty"`
	Status            InferenceServiceStatus `json:"status,omitempty"`
}

type InferenceServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceService `json:"items"`
}

var _ runtime.Object = (*InferenceService)(nil)
var _ runtime.Object = (*InferenceServiceList)(nil)
var _ interface {
	metav1.Object
	runtime.Object
} = (*InferenceService)(nil)
