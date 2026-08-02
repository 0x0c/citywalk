// Package connectserver assembles the Connect handlers the phase-one single process serves (CW-0010
// Unit 2, Unit 11) into one HTTP mux. Later passes register the definition, audience, and delivery
// services' handlers alongside HealthServer here.
package connectserver

import (
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/0x0c/citywalk/gen/citywalk/admin/v1/adminv1connect"
	"github.com/0x0c/citywalk/gen/citywalk/channel/v1/channelv1connect"
	"github.com/0x0c/citywalk/gen/citywalk/delivery/v1/deliveryv1connect"
	"github.com/0x0c/citywalk/gen/citywalk/event/v1/eventv1connect"
	"github.com/0x0c/citywalk/gen/citywalk/platform/v1/platformv1connect"
	"github.com/0x0c/citywalk/internal/platform/adminauth"
	"github.com/0x0c/citywalk/internal/platform/observability"
)

// serviceName identifies this process to the logger NewMux builds for its request-boundary logging,
// matching the name main.go gives observability.Setup for traces and metrics (CW-0010 Unit 11: one
// process, one service identity, in phase one).
const serviceName = "citywalk-server"

// NewMux builds the HTTP handler serving every Connect service the process hosts, instrumented with
// OpenTelemetry so every request carries the trace and metric signals CW-0010 Unit 10 requires, and
// with loggingInterceptor so a handler error also reaches that unit's structured-log signal at the
// same request boundary. pool
// and redisClient may be nil (both are optional in phase one, per CW-0010 Unit 11). DeliveryService
// is registered whenever pool is set, since Confirm needs only Postgres; Sync additionally needs
// redisClient and reports so per call (see DeliveryServer.Sync) rather than the whole service being
// unavailable for want of the one dependency Confirm doesn't need. EventService follows the same
// pattern: registered whenever pool is set, with Submit itself reporting Unavailable if redisClient
// is nil, since only the rate limit (not the log append) needs Redis.
//
// DeliveryService and EventService are additionally wrapped with deviceAuthInterceptor
// (tokenSigningSecret), CW-0010 Unit 9's device authentication: every request through either service
// must carry a bearer token this process issued, and the handlers act only on the channel identity
// bound to that token, never on a request-body field. ChannelService is deliberately NOT wrapped with
// it — Register has no token yet and RefreshToken presents a long-lived credential, not a token —
// and is registered whenever both pool and tokenSigningSecret are set. AdminService is registered
// whenever both pool and adminAuthenticator are set, wrapped with adminAuthInterceptor for CW-0010
// Unit 9's administrative half (identity-provider authentication, role-based authorization).
func NewMux(
	pool *pgxpool.Pool,
	redisClient *redis.Client,
	tokenSigningSecret []byte,
	adminAuthenticator adminauth.Authenticator,
) (http.Handler, error) {
	otelInterceptor, err := otelconnect.NewInterceptor()
	if err != nil {
		return nil, err
	}
	logger := observability.NewLogger(serviceName)
	interceptors := connect.WithInterceptors(otelInterceptor, loggingInterceptor(logger))

	mux := http.NewServeMux()
	healthPath, healthHandler := platformv1connect.NewHealthServiceHandler(HealthServer{}, interceptors)
	mux.Handle(healthPath, healthHandler)

	if pool != nil && len(tokenSigningSecret) > 0 {
		deviceInterceptors := connect.WithInterceptors(otelInterceptor, loggingInterceptor(logger), deviceAuthInterceptor(tokenSigningSecret))

		deliveryPath, deliveryHandler := deliveryv1connect.NewDeliveryServiceHandler(
			DeliveryServer{Pool: pool, Redis: redisClient}, deviceInterceptors,
		)
		mux.Handle(deliveryPath, deliveryHandler)

		eventPath, eventHandler := eventv1connect.NewEventServiceHandler(
			EventServer{Pool: pool, Redis: redisClient}, deviceInterceptors,
		)
		mux.Handle(eventPath, eventHandler)

		channelPath, channelHandler := channelv1connect.NewChannelServiceHandler(
			ChannelServer{Pool: pool, Secret: tokenSigningSecret}, interceptors,
		)
		mux.Handle(channelPath, channelHandler)
	}

	if pool != nil && adminAuthenticator != nil {
		adminInterceptors := connect.WithInterceptors(otelInterceptor, loggingInterceptor(logger), adminAuthInterceptor(adminAuthenticator, adminRoleByProcedure))
		adminPath, adminHandler := adminv1connect.NewAdminServiceHandler(
			AdminServer{Pool: pool, Redis: redisClient}, adminInterceptors,
		)
		mux.Handle(adminPath, adminHandler)
	}

	return mux, nil
}
