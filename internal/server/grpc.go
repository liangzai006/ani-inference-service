package server

import (
	"github.com/go-kratos/kratos/v3/middleware"
	kratosgrpc "github.com/go-kratos/kratos/v3/transport/grpc"

	"github.com/liangzai006/ani-inference-service/internal/conf"
)

func NewGRPCServer(c *conf.Server_GRPC, middlewares ...middleware.Middleware) *kratosgrpc.Server {
	return kratosgrpc.NewServer(
		kratosgrpc.Network(c.Network),
		kratosgrpc.Address(c.Addr),
		kratosgrpc.Timeout(c.Timeout.AsDuration()),
		kratosgrpc.Middleware(middlewares...),
		kratosgrpc.DisableReflection(),
	)
}
