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

	"github.com/0x0c/citywalk/gen/citywalk/delivery/v1/deliveryv1connect"
	"github.com/0x0c/citywalk/gen/citywalk/event/v1/eventv1connect"
	"github.com/0x0c/citywalk/gen/citywalk/platform/v1/platformv1connect"
)

// NewMux builds the HTTP handler serving every Connect service the process hosts, instrumented with
// OpenTelemetry so every request carries the trace and metric signals CW-0010 Unit 10 requires. pool
// and redisClient may be nil (both are optional in phase one, per CW-0010 Unit 11). DeliveryService
// is registered whenever pool is set, since Confirm needs only Postgres; Sync additionally needs
// redisClient and reports so per call (see DeliveryServer.Sync) rather than the whole service being
// unavailable for want of the one dependency Confirm doesn't need. EventService follows the same
// pattern: registered whenever pool is set, with Submit itself reporting Unavailable if redisClient
// is nil, since only the rate limit (not the log append) needs Redis.
func NewMux(pool *pgxpool.Pool, redisClient *redis.Client) (http.Handler, error) {
	otelInterceptor, err := otelconnect.NewInterceptor()
	if err != nil {
		return nil, err
	}
	interceptors := connect.WithInterceptors(otelInterceptor)

	mux := http.NewServeMux()
	healthPath, healthHandler := platformv1connect.NewHealthServiceHandler(HealthServer{}, interceptors)
	mux.Handle(healthPath, healthHandler)

	if pool != nil {
		deliveryPath, deliveryHandler := deliveryv1connect.NewDeliveryServiceHandler(
			DeliveryServer{Pool: pool, Redis: redisClient}, interceptors,
		)
		mux.Handle(deliveryPath, deliveryHandler)

		eventPath, eventHandler := eventv1connect.NewEventServiceHandler(
			EventServer{Pool: pool, Redis: redisClient}, interceptors,
		)
		mux.Handle(eventPath, eventHandler)
	}

	return mux, nil
}
