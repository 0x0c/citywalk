package connectserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/protobuf/types/known/timestamppb"

	channelv1 "github.com/0x0c/citywalk/gen/citywalk/channel/v1"
	"github.com/0x0c/citywalk/internal/channel/register"
)

// ChannelServer implements channelv1connect.ChannelServiceHandler: Register and RefreshToken
// (CW-0010 Unit 9, FR-API-01).
type ChannelServer struct {
	Pool   *pgxpool.Pool
	Secret []byte
}

func (s ChannelServer) Register(
	ctx context.Context,
	req *connect.Request[channelv1.RegisterRequest],
) (*connect.Response[channelv1.RegisterResponse], error) {
	// attrs defaults to an empty (non-nil) map: channels.attributes is NOT NULL, and a device that
	// registers with no attributes yet (FR-API-01 permits updating them later) still needs a row.
	attrs := map[string]any{}
	if raw := req.Msg.GetAttributesJson(); len(raw) > 0 {
		if err := json.Unmarshal(raw, &attrs); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("decode attributes_json: %w", err))
		}
	}
	supportedMajor := int(req.Msg.GetSupportedSchemaMajor())
	if supportedMajor <= 0 {
		supportedMajor = 1
	}

	result, err := register.Register(ctx, s.Pool, s.Secret, attrs, supportedMajor, time.Now())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&channelv1.RegisterResponse{
		ChannelId:            result.ChannelID,
		Credential:           result.Credential,
		AccessToken:          result.AccessToken,
		AccessTokenExpiresAt: timestamppb.New(result.ExpiresAt),
	}), nil
}

func (s ChannelServer) RefreshToken(
	ctx context.Context,
	req *connect.Request[channelv1.RefreshTokenRequest],
) (*connect.Response[channelv1.RefreshTokenResponse], error) {
	result, err := register.RefreshToken(ctx, s.Pool, s.Secret, req.Msg.GetChannelId(), req.Msg.GetCredential(), time.Now())
	if err != nil {
		if errors.Is(err, register.ErrInvalidCredential) {
			return nil, connect.NewError(connect.CodeUnauthenticated, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return connect.NewResponse(&channelv1.RefreshTokenResponse{
		AccessToken:          result.AccessToken,
		AccessTokenExpiresAt: timestamppb.New(result.ExpiresAt),
	}), nil
}
