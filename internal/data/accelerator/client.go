// Package accelerator contains the optional service-to-service adapter for
// ani-accelerator-service. The adapter is deliberately separate from the
// inference domain and is only constructed when GPU resolution is enabled.
package accelerator

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	acceleratorv1 "github.com/zhangzhe-ctrl/ani-accelerator-service/api/gen/go/accelerator/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
)

var (
	ErrResolverNotConfigured = errors.New("accelerator resolver is not configured")
	ErrUsageNotConfigured    = errors.New("accelerator usage observer is not configured")
	ErrClusterNotConfigured  = errors.New("accelerator cluster identity is not configured")
	ErrInvalidTLSConfig      = errors.New("invalid accelerator TLS configuration")
	ErrInvalidActor          = errors.New("accelerator actor must be type:id")
)

// TLSConfig describes the accelerator server connection. There is no
// insecure/plaintext mode: all configured connections require TLS 1.3 and a
// client certificate signed by the configured CA.
type TLSConfig struct {
	CAFile     string
	CertFile   string
	KeyFile    string
	ServerName string
}

// Validate checks the complete client identity configuration before a dial.
func (c TLSConfig) Validate() error {
	if c.CAFile == "" || c.CertFile == "" || c.KeyFile == "" || c.ServerName == "" {
		return ErrInvalidTLSConfig
	}
	return nil
}

// Config is intentionally opt-in. An empty Config is valid only when no GPU
// request is sent; Dial rejects it so a caller cannot accidentally create a
// plaintext or unauthenticated dependency.
type Config struct {
	Endpoint string
	// ClusterID fences this inference process to the newly configured
	// Kubernetes cluster. Empty is allowed for local contract tests only.
	ClusterID string
	TLS       TLSConfig
}

// Client resolves explicit GPU requests through the accelerator catalog.
type Client struct {
	catalog   acceleratorv1.AcceleratorCatalogServiceClient
	usage     acceleratorv1.AcceleratorUsageServiceClient
	conn      *grpc.ClientConn
	clusterID string
}

// NewClient wraps an already-created catalog client. It is useful for tests
// and for applications that own connection construction. The adapter never
// contacts the service when input.Request is nil.
func NewClient(catalog acceleratorv1.AcceleratorCatalogServiceClient) *Client {
	return &Client{catalog: catalog}
}

// NewClientForCluster wraps a test or already-owned connection and enables
// the same cluster fence used by the production Dial path.
func NewClientForCluster(catalog acceleratorv1.AcceleratorCatalogServiceClient, clusterID string) *Client {
	return &Client{catalog: catalog, clusterID: strings.TrimSpace(clusterID)}
}

// Dial creates a TLS-only accelerator client. The returned close function is
// safe to call once by the composition root that owns the connection.
func Dial(ctx context.Context, cfg Config) (*Client, func() error, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, nil, ErrResolverNotConfigured
	}
	if err := cfg.TLS.Validate(); err != nil {
		return nil, nil, err
	}
	caPEM, err := os.ReadFile(cfg.TLS.CAFile)
	if err != nil {
		return nil, nil, fmt.Errorf("read accelerator CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, nil, fmt.Errorf("%w: CA bundle has no certificates", ErrInvalidTLSConfig)
	}
	cert, err := tls.LoadX509KeyPair(cfg.TLS.CertFile, cfg.TLS.KeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load accelerator client certificate: %w", err)
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		RootCAs:      pool,
		Certificates: []tls.Certificate{cert},
		ServerName:   cfg.TLS.ServerName,
		// The server's exact service identity is verified by its certificate
		// and CA. No InsecureSkipVerify or plaintext fallback is permitted.
		InsecureSkipVerify: false,
	}
	conn, err := grpc.DialContext(ctx, cfg.Endpoint,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
		grpc.WithBlock(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("dial accelerator service: %w", err)
	}
	return &Client{
		catalog:   acceleratorv1.NewAcceleratorCatalogServiceClient(conn),
		usage:     acceleratorv1.NewAcceleratorUsageServiceClient(conn),
		conn:      conn,
		clusterID: strings.TrimSpace(cfg.ClusterID),
	}, conn.Close, nil
}

// Resolve implements gpu.Resolver. A nil request is a no-op by design; this
// makes the adapter safe to keep in a composition root when a service is CPU
// only. A non-nil request with no catalog is an error and is never downgraded
// to CPU placement.
func (c *Client) Resolve(ctx context.Context, input gpu.ResolveInput) (*gpu.Plan, error) {
	if input.Request == nil {
		return nil, nil
	}
	if err := gpu.ValidateRequest(input.Request); err != nil {
		return nil, fmt.Errorf("validate GPU request: %w", err)
	}
	if c == nil {
		return nil, ErrResolverNotConfigured
	}
	if c.clusterID == "" {
		return nil, ErrClusterNotConfigured
	}
	if c.clusterID != input.Request.ClusterID {
		return nil, fmt.Errorf("GPU request cluster_id %q does not match configured cluster", input.Request.ClusterID)
	}
	if c.catalog == nil {
		return nil, ErrResolverNotConfigured
	}
	actorType, actorID, err := parseActor(input.Actor)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(input.TenantID) == "" || strings.TrimSpace(input.RequestID) == "" {
		return nil, fmt.Errorf("accelerator tenant and request ID are required")
	}
	ctx = metadata.AppendToOutgoingContext(ctx,
		"x-ani-tenant-id", input.TenantID,
		"x-ani-action", "ResolveGpuRequest",
		"x-ani-actor-type", actorType,
		"x-ani-actor-id", actorID,
	)
	response, err := c.catalog.ResolveGpuRequest(ctx, &acceleratorv1.ResolveGpuRequestRequest{
		Context: &acceleratorv1.TenantContext{
			RequestId: input.RequestID,
			TenantId:  input.TenantID,
			Actor:     &acceleratorv1.Actor{Type: actorType, Id: actorID},
		},
		Gpu: toProtoRequest(input.Request),
	})
	if err != nil {
		return nil, err
	}
	plan, err := fromProtoPlan(response)
	if err != nil {
		return nil, err
	}
	if err := gpu.ValidatePlan(plan, input.Request); err != nil {
		return nil, fmt.Errorf("validate resolved GPU plan: %w", err)
	}
	return plan, nil
}

// ObserveRelease asks Accelerator for evidence that the complete Pod UID
// scope no longer owns a GPU allocation. A positive result is evidence only;
// the caller remains responsible for persisting the release event and then
// notifying Governance.
func (c *Client) ObserveRelease(ctx context.Context, input gpu.ReleaseObservationInput) (gpu.ReleaseObservation, error) {
	if c == nil || c.usage == nil {
		return gpu.ReleaseObservation{}, ErrUsageNotConfigured
	}
	if err := gpu.ValidateReleaseObservationInput(input); err != nil {
		return gpu.ReleaseObservation{}, err
	}
	if c.clusterID == "" {
		return gpu.ReleaseObservation{}, ErrClusterNotConfigured
	}
	if c.clusterID != input.ClusterID {
		return gpu.ReleaseObservation{}, fmt.Errorf("release cluster_id %q does not match configured cluster", input.ClusterID)
	}
	actorType, actorID, err := parseActor(input.Actor)
	if err != nil {
		return gpu.ReleaseObservation{}, err
	}
	ctx = metadata.AppendToOutgoingContext(ctx,
		"x-ani-tenant-id", input.TenantID,
		"x-ani-action", "ObserveRelease",
		"x-ani-actor-type", actorType,
		"x-ani-actor-id", actorID,
	)
	pods := make([]*acceleratorv1.PodRef, 0, len(input.Pods))
	for _, pod := range input.Pods {
		pod := pod
		pods = append(pods, &acceleratorv1.PodRef{ClusterId: pod.ClusterID, Namespace: pod.Namespace, Name: pod.Name, Uid: pod.UID, ContainerName: pod.ContainerName})
	}
	response, err := c.usage.ObserveRelease(ctx, &acceleratorv1.ObserveReleaseRequest{
		RequestId: input.RequestID,
		Ref: &acceleratorv1.GpuUsageRef{
			TenantId: input.TenantID, OwnerService: input.OwnerService,
			ResourceId: input.ResourceID, CreateOperationId: input.CreateOperationID,
		},
		DeleteOperationId: input.DeleteOperationID,
		Pods:              pods,
	})
	if err != nil {
		return gpu.ReleaseObservation{}, err
	}
	if response == nil {
		return gpu.ReleaseObservation{}, errors.New("accelerator release observation is empty")
	}
	finding := gpu.ReleaseFindingUnknown
	switch response.GetFinding() {
	case acceleratorv1.ReleaseFinding_ALLOCATION_RELEASED:
		finding = gpu.ReleaseFindingReleased
	case acceleratorv1.ReleaseFinding_ALLOCATION_STILL_PRESENT:
		finding = gpu.ReleaseFindingStillPresent
	case acceleratorv1.ReleaseFinding_RELEASE_UNKNOWN, acceleratorv1.ReleaseFinding_RELEASE_FINDING_UNSPECIFIED:
		finding = gpu.ReleaseFindingUnknown
	}
	return gpu.ReleaseObservation{
		EvidenceID:        response.GetEvidenceId(),
		EvidenceDigest:    response.GetEvidenceDigest(),
		Finding:           finding,
		ActivePodUIDs:     append([]string(nil), response.GetActivePodUids()...),
		UncertainNodeUIDs: append([]string(nil), response.GetUncertainNodeUids()...),
	}, nil
}

func parseActor(actor string) (string, string, error) {
	actor = strings.TrimSpace(actor)
	typeName, id, ok := strings.Cut(actor, ":")
	if !ok || strings.TrimSpace(typeName) == "" || strings.TrimSpace(id) == "" {
		return "", "", ErrInvalidActor
	}
	return typeName, id, nil
}
