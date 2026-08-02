package connectserver

import (
	"context"
	"errors"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/0x0c/citywalk/internal/platform/adminauth"
	"github.com/0x0c/citywalk/internal/platform/devicetoken"
)

type channelIDContextKey struct{}
type principalContextKey struct{}

// withChannelID and channelIDFromContext carry CW-0010 Unit 9's device-bound identity through a
// request: "the identifier in the request is ignored in favor of the one in the token." A handler
// that reads the channel ID from here rather than from its request message cannot be made to act on
// a channel the caller's token doesn't name, no matter what the request body claims.
func withChannelID(ctx context.Context, channelID string) context.Context {
	return context.WithValue(ctx, channelIDContextKey{}, channelID)
}

func channelIDFromContext(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(channelIDContextKey{}).(string)
	return v, ok
}

func withPrincipal(ctx context.Context, p adminauth.Principal) context.Context {
	return context.WithValue(ctx, principalContextKey{}, p)
}

func principalFromContext(ctx context.Context) (adminauth.Principal, bool) {
	v, ok := ctx.Value(principalContextKey{}).(adminauth.Principal)
	return v, ok
}

// bearerToken extracts the credential from a request's Authorization header, stripping the "Bearer "
// prefix devices and admin clients alike are expected to send it with.
func bearerToken(header connect.AnyRequest) string {
	auth := header.Header().Get("Authorization")
	return strings.TrimPrefix(auth, "Bearer ")
}

// deviceAuthInterceptor verifies the bearer token on every request through the services it wraps
// (DeliveryService, EventService) and injects the channel identifier it's bound to into the request
// context, per CW-0010 Unit 9. There is no exemption inside this interceptor for any procedure — it
// is applied only to the device-facing services in NewMux, never to ChannelService (whose Register
// and RefreshToken calls are the ones exempt, because a device has no token yet at registration and
// presents its long-lived credential, not a token, when refreshing).
func deviceAuthInterceptor(secret []byte) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			token := bearerToken(req)
			if token == "" {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing bearer token"))
			}
			channelID, err := devicetoken.Verify(secret, token, time.Now())
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}
			return next(withChannelID(ctx, channelID), req)
		}
	})
}

// adminAuthInterceptor authenticates every request through AdminService against authenticator and
// requires the resulting principal's role to satisfy at least the role requiredRole names for that
// request's procedure (CW-0010 Unit 9: "authorizes by role"). requiredRoleByProcedure maps a
// procedure's full name (connect.AnyRequest.Spec().Procedure, e.g.
// "/citywalk.admin.v1.AdminService/CreateMessage") to the role it requires; a procedure absent from
// the map is rejected rather than defaulting to some role, since a missing entry is a configuration
// bug this interceptor should surface loudly rather than paper over with a guessed default.
func adminAuthInterceptor(authenticator adminauth.Authenticator, requiredRoleByProcedure map[string]adminauth.Role) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			requiredRole, ok := requiredRoleByProcedure[req.Spec().Procedure]
			if !ok {
				return nil, connect.NewError(connect.CodeInternal, errors.New("adminAuthInterceptor: no role requirement configured for this procedure"))
			}

			principal, err := authenticator.Authenticate(ctx, bearerToken(req))
			if err != nil {
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}
			if !principal.Role.Satisfies(requiredRole) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("adminAuthInterceptor: role does not satisfy this operation's requirement"))
			}

			return next(withPrincipal(ctx, principal), req)
		}
	})
}
