package server

import (
	"context"
	"log/slog"

	"github.com/go-kratos/kratos/v3/middleware"
	"github.com/go-kratos/kratos/v3/middleware/logging"
	inferencev1 "github.com/liangzai006/ani-inference-service/api/inference/v1"
	"google.golang.org/grpc/metadata"
)

// probeLogHandler suppresses successful probe records only. Errors and all
// business RPCs remain visible, and the HTTP handlers and metrics still run.
type probeLogHandler struct{ slog.Handler }

func (h probeLogHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message == "server request" && record.Level == slog.LevelInfo {
		var operation, component string
		var code int64
		record.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "operation":
				operation = a.Value.String()
			case "component":
				component = a.Value.String()
			case "code":
				code = a.Value.Int64()
			}
			return true
		})
		if component == "http" && code == 200 && (operation == "/healthz" || operation == "/readyz" || operation == "/metrics") {
			return nil
		}
	}
	return h.Handler.Handle(ctx, record)
}

func safeLogID(id string) string {
	if len(id) == 0 || len(id) > 128 {
		return ""
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return ""
		}
	}
	return id
}

func requestLogging(logger *slog.Logger) middleware.Middleware {
	if logger == nil {
		logger = slog.Default()
	}
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (any, error) {
			requestLogger := slog.New(probeLogHandler{logger.Handler()})
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				for _, key := range []string{"x-ani-request-id", "x-ani-origin-request-id"} {
					values := md.Get(key)
					if len(values) == 1 {
						if id := safeLogID(values[0]); id != "" {
							field := "request_id"
							if key == "x-ani-origin-request-id" {
								field = "origin_request_id"
							}
							requestLogger = requestLogger.With(field, id)
						}
					}
				}
			}
			if managed, ok := req.(interface {
				GetContext() *inferencev1.CommandContext
			}); ok {
				if id := safeLogID(managed.GetContext().GetOperationId()); id != "" {
					requestLogger = requestLogger.With("operation_id", id)
				}
			}
			return logging.Server(requestLogger)(next)(ctx, req)
		}
	}
}
