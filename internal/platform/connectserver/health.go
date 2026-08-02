package connectserver

import (
	"context"

	"connectrpc.com/connect"

	platformv1 "github.com/0x0c/citywalk/gen/citywalk/platform/v1"
)

// HealthServer implements platformv1connect.HealthServiceHandler. It reports SERVING
// unconditionally in phase one, where the process has nothing to be ready-checked against beyond
// its own liveness; a readiness check against Postgres and Redis is a later pass's job once the
// services backed by them exist.
type HealthServer struct{}

func (HealthServer) Check(
	_ context.Context,
	_ *connect.Request[platformv1.CheckRequest],
) (*connect.Response[platformv1.CheckResponse], error) {
	return connect.NewResponse(&platformv1.CheckResponse{
		Status: platformv1.ServingStatus_SERVING_STATUS_SERVING,
	}), nil
}
