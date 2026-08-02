package connectserver

import (
	"context"
	"log/slog"

	"connectrpc.com/connect"
)

// loggingInterceptor logs every handler error at the request boundary, labeled by the procedure that
// produced it — CW-0010 Unit 10's structured logging, applied at the same seam otelInterceptor already
// covers with tracing and metrics, so a failing procedure shows up in the log stream the same way it
// shows up in a trace.
func loggingInterceptor(logger *slog.Logger) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			resp, err := next(ctx, req)
			if err != nil {
				logger.ErrorContext(ctx, "request failed",
					slog.String("procedure", req.Spec().Procedure),
					slog.Any("error", err),
				)
			}
			return resp, err
		}
	})
}
