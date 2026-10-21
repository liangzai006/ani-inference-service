package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	kratos "github.com/go-kratos/kratos/v3"
	"github.com/go-kratos/kratos/v3/middleware"
	kratosTransport "github.com/go-kratos/kratos/v3/transport"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	kratoshttp "github.com/go-kratos/kratos/v3/transport/http"
	"github.com/liangzai006/ani-inference-service/internal/conf"

	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	diagnosticsbiz "github.com/liangzai006/ani-inference-service/internal/biz/diagnostics"
	"github.com/liangzai006/ani-inference-service/internal/biz/gpu"
	inferencebiz "github.com/liangzai006/ani-inference-service/internal/biz/inference"
	"github.com/liangzai006/ani-inference-service/internal/biz/work"
	"github.com/liangzai006/ani-inference-service/internal/server"
	"github.com/liangzai006/ani-inference-service/internal/service"
)

func buildAppWithAllDependenciesAndBackground(bc *conf.Bootstrap, logger *slog.Logger, create service.CreateUseCase, read inferencebiz.ReadUseCase, command inferencebiz.CommandUseCase, update inferencebiz.UpdateUseCase, background ...kratosTransport.Server) (*kratos.App, error) {
	return buildAppWithAllDependenciesAndManagedGPU(bc, logger, create, read, command, update, nil, false, "", background...)
}

func buildAppWithAllDependenciesAndManagedGPU(bc *conf.Bootstrap, logger *slog.Logger, create service.CreateUseCase, read inferencebiz.ReadUseCase, command inferencebiz.CommandUseCase, update inferencebiz.UpdateUseCase, refund gpu.RefundReporter, managedGPUReady bool, managedGPUClusterID string, background ...kratosTransport.Server) (*kratos.App, error) {
	if err := bc.Validate(); err != nil {
		return nil, err
	}
	if _, enabled, err := governanceRefundConfig(bc.GetManagedGpu()); err != nil {
		return nil, err
	} else if enabled && refund == nil {
		return nil, errors.New("managed GPU refund requires the real reporter")
	}
	readiness := server.NewReadiness()
	observability, err := server.NewObservability(Name, Version, readiness)
	if err != nil {
		return nil, err
	}
	// Keep the base middleware set for the admin HTTP server. Health, readiness,
	// and metrics are intentionally probeable without a client certificate.
	// The RPC identity policy below applies only to the gRPC business surface;
	// putting it on the HTTP server makes Kubernetes probes return 401.
	adminMiddlewares := observability.ServerMiddleware(logger)
	middlewares := append([]middleware.Middleware(nil), adminMiddlewares...)

	// Add RPC method authorization policy when mTLS is configured
	// This enforces that only authorized services can call specific methods
	middlewares = append(middlewares, RPCPolicyMiddleware(InferenceServicePolicy()))

	for _, backgroundServer := range background {
		if sourceProvider, ok := backgroundServer.(interface{ MetricsSource() work.MetricsSource }); ok {
			observability.SetMetricsSource(sourceProvider.MetricsSource())
		}
	}
	owner := service.NewInferenceServerWithAll(create, read, command, update)
	owner.SetManagedGPURefundReporter(refund)
	owner.SetManagedGPUReady(managedGPUReady)
	owner.SetManagedGPUClusterID(managedGPUClusterID)
	grpcServer, err := configuredInferenceGRPCServer(bc.Server.Grpc, middlewares...)
	if err != nil {
		_ = observability.Shutdown(context.Background())
		return nil, err
	}
	inferencev1.RegisterInferenceServiceManagerServer(grpcServer, owner)
	diagnostics := &service.DiagnosticsServer{Read: read}
	for _, backgroundServer := range background {
		if provider, ok := backgroundServer.(interface{ DiagnosticsReader() diagnosticsbiz.Reader }); ok {
			diagnostics.Runtime = provider.DiagnosticsReader()
		}
	}
	inferencev1.RegisterInferenceDiagnosticsServer(grpcServer, diagnostics)
	readyOnStart := readyOnStartForBackground(background)
	referenceReader, _ := read.(inferencebiz.ModelReferenceReader)
	inferencev1.RegisterModelReferenceServiceServer(grpcServer, service.NewModelReferenceServer(referenceReader))
	adminServer := server.NewAdminServer(bc.Server.Admin, readiness, observability.Gatherer(), adminMiddlewares...)
	return newApp(logger, grpcServer, adminServer, readiness, observability, bc.Server.ShutdownTimeout.AsDuration(), readyOnStart, background...), nil
}

func readyOnStartForBackground(background []kratosTransport.Server) bool {
	return len(background) == 0
}

func newApp(
	logger *slog.Logger,
	grpcServer *kratosgrpc.Server,
	adminServer *kratoshttp.Server,
	readiness *server.Readiness,
	observability *server.Observability,
	stopTimeout time.Duration,
	readyOnStart bool,
	background ...kratosTransport.Server,
) *kratos.App {
	servers := make([]kratosTransport.Server, 0, 2+len(background))
	servers = append(servers, grpcServer, adminServer)
	servers = append(servers, background...)
	for _, backgroundServer := range background {
		if readyAware, ok := backgroundServer.(interface{ SetReadyCallback(func()) }); ok {
			readyAware.SetReadyCallback(func() { readiness.Set(true) })
		}
	}
	return kratos.New(
		kratos.ID(id),
		kratos.Name(Name),
		kratos.Version(Version),
		kratos.Logger(logger),
		kratos.Server(servers...),
		kratos.AfterStart(func(context.Context) error {
			if readyOnStart {
				readiness.Set(true)
			}
			return nil
		}),
		kratos.BeforeStop(func(context.Context) error {
			readiness.Set(false)
			return nil
		}),
		kratos.AfterStop(observability.Shutdown),
		kratos.StopTimeout(stopTimeout),
	)
}
