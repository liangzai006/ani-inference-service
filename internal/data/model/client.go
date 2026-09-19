// Package model implements Inference's versioned Model catalog client.
package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"time"

	modelv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/model/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Snapshot is deployment input from Model, not an Inference runtime observation.
type Snapshot struct {
	VersionID, ModelID, ArtifactRef, ArtifactSHA256, EngineRuntime string
	ArtifactSizeBytes                                              int64
	CommandArgv                                                    []string
}
type Download struct {
	URL, StoragePath string
	ExpiresAt        time.Time
}
type Client struct{ api modelv1.ModelServiceClient }

func NewClient(api modelv1.ModelServiceClient) *Client { return &Client{api: api} }

func (c *Client) GetReadyVersion(ctx context.Context, tenant, versionID string) (Snapshot, error) {
	if c == nil || c.api == nil {
		return Snapshot{}, status.Error(codes.FailedPrecondition, "Model client is not configured")
	}
	if tenant == "" || versionID == "" {
		return Snapshot{}, status.Error(codes.InvalidArgument, "tenant and model version ID are required")
	}
	r, err := c.api.GetModelVersion(ctx, &modelv1.GetModelVersionRequest{TenantId: tenant, ModelVersionId: versionID})
	if err != nil {
		return Snapshot{}, err
	}
	v := r.GetVersion()
	if v == nil {
		return Snapshot{}, status.Error(codes.NotFound, "Model version missing")
	}
	if v.GetId() != versionID {
		return Snapshot{}, status.Error(codes.DataLoss, "Model returned a different version")
	}
	if v.GetStatus() != "ready" {
		return Snapshot{}, status.Error(codes.FailedPrecondition, "Model version is not ready")
	}
	digest, err := hex.DecodeString(v.GetChecksumSha256())
	if err != nil || len(digest) != sha256.Size || strings.TrimSpace(v.GetStoragePath()) == "" {
		return Snapshot{}, status.Error(codes.DataLoss, "Model artifact is incomplete")
	}
	argv := append([]string(nil), v.GetStartupArgs()...)
	if v.GetStartupCommand() != "" {
		argv = append([]string{v.GetStartupCommand()}, argv...)
	}
	return Snapshot{VersionID: v.GetId(), ModelID: v.GetModelId(), ArtifactRef: v.GetStoragePath(), ArtifactSHA256: v.GetChecksumSha256(), ArtifactSizeBytes: v.GetSizeBytes(), EngineRuntime: v.GetEngineType(), CommandArgv: argv}, nil
}

func (c *Client) GetArtifactDownloadURL(ctx context.Context, tenant, versionID, requester string) (Download, error) {
	if c == nil || c.api == nil {
		return Download{}, status.Error(codes.FailedPrecondition, "Model client is not configured")
	}
	if tenant == "" || versionID == "" {
		return Download{}, status.Error(codes.InvalidArgument, "tenant and model version ID are required")
	}
	r, err := c.api.GetModelDownloadURL(ctx, &modelv1.GetModelDownloadURLRequest{TenantId: tenant, ModelVersionId: versionID, Requester: requester})
	if err != nil {
		return Download{}, err
	}
	if r.GetExpiresAt() == nil || r.GetExpiresAt().CheckValid() != nil {
		return Download{}, status.Error(codes.DataLoss, "Model returned invalid expiry")
	}
	u, err := url.Parse(r.GetDownloadUrl())
	exp := r.GetExpiresAt().AsTime()
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || r.GetStoragePath() == "" || !exp.After(time.Now()) {
		return Download{}, status.Error(codes.DataLoss, "Model returned invalid or expired download URL")
	}
	return Download{URL: r.GetDownloadUrl(), StoragePath: r.GetStoragePath(), ExpiresAt: exp}, nil
}
