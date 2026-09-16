package main

import (
	"context"
	"log/slog"
	"time"

	kratos "github.com/go-kratos/kratos/v3"
	kratosTransport "github.com/go-kratos/kratos/v3/transport"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"
	kratoshttp "github.com/go-kratos/kratos/v3/transport/http"

	inferencev1 "github.com/zhangzhe-ctrl/ani-inference-service/api/inference/v1"
	inferencebiz "github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/inference"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/biz/work"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/server"
	"github.com/zhangzhe-ctrl/ani-inference-service/internal/service"
)

func buildApp(bc *inferencev1.Bootstrap, logger *slog.Logger, create ...service.CreateUseCase) (*kratos.App, error) {
	if err := bc.Validate(); err != nil {
		return nil, err
	}
	readiness := server.NewReadiness()
	observability, err := server.NewObservability(Name, Version, readiness)
	if err != nil {
		return nil, err
	}
	middlewares := observability.ServerMiddleware(logger)
	grpcServer := server.NewGRPCServer(bc.Server.Grpc, middlewares...)
	inferencev1.RegisterInferenceServiceManagerServer(grpcServer, service.NewInferenceServer(create...))
	adminServer := server.NewAdminServer(bc.Server.Admin, readiness, observability.Gatherer(), middlewares...)
	// The dependency-free layout process is an explicitly non-business mode;
	// keep its process readiness contract for health/lifecycle checks.
	return newApp(logger, grpcServer, adminServer, readiness, observability, bc.Server.ShutdownTimeout.AsDuration(), true), nil
}

func buildAppWithRead(bc *inferencev1.Bootstrap, logger *slog.Logger, create service.CreateUseCase, read inferencebiz.ReadUseCase) (*kratos.App, error) {
	return buildAppWithDependencies(bc, logger, create, read, nil)
}

func buildAppWithDependencies(bc *inferencev1.Bootstrap, logger *slog.Logger, create service.CreateUseCase, read inferencebiz.ReadUseCase, command inferencebiz.CommandUseCase) (*kratos.App, error) {
	return buildAppWithAllDependencies(bc, logger, create, read, command, nil)
}

func buildAppWithAllDependencies(bc *inferencev1.Bootstrap, logger *slog.Logger, create service.CreateUseCase, read inferencebiz.ReadUseCase, command inferencebiz.CommandUseCase, update inferencebiz.UpdateUseCase) (*kratos.App, error) {
	return buildAppWithAllDependenciesAndBackground(bc, logger, create, read, command, update)
}

func buildAppWithAllDependenciesAndBackground(bc *inferencev1.Bootstrap, logger *slog.Logger, create service.CreateUseCase, read inferencebiz.ReadUseCase, command inferencebiz.CommandUseCase, update inferencebiz.UpdateUseCase, background ...kratosTransport.Server) (*kratos.App, error) {
	if err := bc.Validate(); err != nil {
		return nil, err
	}
	readiness := server.NewReadiness()
	observability, err := server.NewObservability(Name, Version, readiness)
	if err != nil {
		return nil, err
	}
	middlewares := observability.ServerMiddleware(logger)
	for _, backgroundServer := range background {
		if sourceProvider, ok := backgroundServer.(interface{ MetricsSource() work.MetricsSource }); ok {
			observability.SetMetricsSource(sourceProvider.MetricsSource())
		}
	}
	grpcServer := server.NewGRPCServer(bc.Server.Grpc, middlewares...)
	inferencev1.RegisterInferenceServiceManagerServer(grpcServer, service.NewInferenceServerWithAll(create, read, command, update))
	adminServer := server.NewAdminServer(bc.Server.Admin, readiness, observability.Gatherer(), middlewares...)
	// A PostgreSQL-backed process without a durable worker must stay unready:
	// otherwise it can accept commands that are persisted forever without an
	// executor. A background runtime promotes readiness only after its own
	// dependency gate succeeds. The dependency-free layout process remains
	// ready for health/lifecycle checks.
	readyOnStart := initialReadiness(create != nil, read != nil, command != nil, update != nil, len(background) > 0)
	return newApp(logger, grpcServer, adminServer, readiness, observability, bc.Server.ShutdownTimeout.AsDuration(), readyOnStart, background...), nil
}

func initialReadiness(create, read, command, update, hasBackground bool) bool {
	if hasBackground {
		return false
	}
	return !create && !read && !command && !update
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
