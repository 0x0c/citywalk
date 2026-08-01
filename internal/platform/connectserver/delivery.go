package connectserver

import (
	"context"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"

	deliveryv1 "github.com/0x0c/citywalk/gen/citywalk/delivery/v1"
	"github.com/0x0c/citywalk/internal/delivery/confirm"
)

// DeliveryServer implements deliveryv1connect.DeliveryServiceHandler's Confirm RPC (CW-0002 Unit 4).
type DeliveryServer struct {
	Pool *pgxpool.Pool
}

func (s DeliveryServer) Confirm(
	ctx context.Context,
	req *connect.Request[deliveryv1.ConfirmRequest],
) (*connect.Response[deliveryv1.ConfirmResponse], error) {
	approved, err := confirm.Confirm(ctx, s.Pool, req.Msg.GetMessageId(), time.Now())
	if err != nil {
		// Propagated rather than swallowed into a false-but-200-OK response: FR-API-08 and CW-0002
		// Unit 4 both specify that an error suppresses the display exactly like an explicit no, so
		// there is no correctness reason to hide this from the caller — and hiding it would also
		// hide it from the OpenTelemetry interceptor wrapping this handler.
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&deliveryv1.ConfirmResponse{Approved: approved}), nil
}
