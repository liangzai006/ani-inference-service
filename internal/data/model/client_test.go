package model

import (
	"context"
	"strings"
	"testing"
	"time"

	modelv1 "github.com/zhangzhe-ctrl/ani-inference-service/api/model/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type modelAPIFake struct {
	modelv1.ModelServiceClient
	version  *modelv1.ModelVersion
	download *modelv1.GetModelDownloadURLResponse
	request  *modelv1.GetModelVersionRequest
	err      error
}

func (f *modelAPIFake) GetModelVersion(_ context.Context, r *modelv1.GetModelVersionRequest, _ ...grpc.CallOption) (*modelv1.GetModelVersionResponse, error) {
	f.request = r
	return &modelv1.GetModelVersionResponse{Version: f.version}, f.err
}
func (f *modelAPIFake) GetModelDownloadURL(context.Context, *modelv1.GetModelDownloadURLRequest, ...grpc.CallOption) (*modelv1.GetModelDownloadURLResponse, error) {
	return f.download, f.err
}
func readyVersion() *modelv1.ModelVersion {
	return &modelv1.ModelVersion{Id: "version-id", ModelId: "external-model", Status: "ready", StoragePath: "tenant/model", ChecksumSha256: strings.Repeat("a", 64), SizeBytes: 3 << 30, EngineType: "vllm", StartupCommand: "vllm", StartupArgs: []string{"serve"}}
}
func TestGetReadyVersionMapsExplicitVersionAndDefaults(t *testing.T) {
	api := &modelAPIFake{version: readyVersion()}
	got, err := NewClient(api).GetReadyVersion(context.Background(), "tenant-id", "version-id")
	if err != nil {
		t.Fatal(err)
	}
	if api.request.GetTenantId() != "tenant-id" || api.request.GetModelVersionId() != "version-id" {
		t.Fatalf("request=%v", api.request)
	}
	if got.VersionID != "version-id" || got.ModelID != "external-model" || got.ArtifactRef != "tenant/model" || got.ArtifactSHA256 != strings.Repeat("a", 64) || got.EngineRuntime != "vllm" || got.ArtifactSizeBytes != 3<<30 || strings.Join(got.CommandArgv, " ") != "vllm serve" {
		t.Fatalf("snapshot=%+v", got)
	}
	api.version.StartupCommand = ""
	got, err = NewClient(api).GetReadyVersion(context.Background(), "tenant-id", "version-id")
	if err != nil || len(got.CommandArgv) != 1 || got.CommandArgv[0] != "serve" {
		t.Fatalf("empty command mapping=%+v err=%v", got, err)
	}
}
func TestGetReadyVersionRejectsInvalidSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*modelv1.ModelVersion)
	}{
		{"pending", func(v *modelv1.ModelVersion) { v.Status = "pending" }},
		{"wrong version", func(v *modelv1.ModelVersion) { v.Id = "operation-id" }},
		{"missing artifact", func(v *modelv1.ModelVersion) { v.StoragePath = "" }},
		{"invalid digest", func(v *modelv1.ModelVersion) { v.ChecksumSha256 = strings.Repeat("z", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := proto.Clone(readyVersion()).(*modelv1.ModelVersion)
			tc.mutate(v)
			_, err := NewClient(&modelAPIFake{version: v}).GetReadyVersion(context.Background(), "tenant", "version-id")
			if err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
}
func TestClientPreservesRemoteErrors(t *testing.T) {
	for _, code := range []codes.Code{codes.NotFound, codes.Unauthenticated, codes.PermissionDenied, codes.Unavailable} {
		c := NewClient(&modelAPIFake{err: status.Error(code, "provider failed")})
		_, err := c.GetReadyVersion(context.Background(), "tenant", "version-id")
		if status.Code(err) != code {
			t.Fatalf("version status=%v want=%v", status.Code(err), code)
		}
		_, err = c.GetArtifactDownloadURL(context.Background(), "tenant", "version-id", "inference")
		if status.Code(err) != code {
			t.Fatalf("download status=%v want=%v", status.Code(err), code)
		}
	}
}
func TestDownloadRejectsInvalidResponse(t *testing.T) {
	for _, r := range []*modelv1.GetModelDownloadURLResponse{nil, {}, {DownloadUrl: "https://storage/model", StoragePath: "tenant/model", ExpiresAt: timestamppb.New(time.Now().Add(-time.Second))}, {DownloadUrl: "file:///etc/passwd", StoragePath: "tenant/model", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}} {
		if _, err := NewClient(&modelAPIFake{download: r}).GetArtifactDownloadURL(context.Background(), "tenant", "version-id", "inference"); err == nil {
			t.Fatal("invalid URL accepted")
		}
	}
	c := NewClient(&modelAPIFake{download: &modelv1.GetModelDownloadURLResponse{DownloadUrl: "https://storage/model", StoragePath: "tenant/model", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}})
	if _, err := c.GetArtifactDownloadURL(context.Background(), "tenant", "version-id", "inference"); err != nil {
		t.Fatal(err)
	}
}
