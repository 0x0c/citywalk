// Package connectserver assembles the Connect handlers the phase-one single process serves (CW-0010
// Unit 2, Unit 11) into one HTTP mux. Later passes register the definition, audience, and delivery
// services' handlers alongside HealthServer here.
package connectserver

import (
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/0x0c/citywalk/gen/citywalk/delivery/v1/deliveryv1connect"
	"github.com/0x0c/citywalk/gen/citywalk/platform/v1/platformv1connect"
)

// NewMux builds the HTTP handler serving every Connect service the process hosts, instrumented with
// OpenTelemetry so every request carries the trace and metric signals CW-0010 Unit 10 requires. pool
// may be nil (Postgres is optional in phase one, per CW-0010 Unit 11); DeliveryService is registered
// only when it is set, since Confirm has no way to answer without a database to check against.
func NewMux(pool *pgxpool.Pool) (http.Handler, error) {
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
			DeliveryServer{Pool: pool}, interceptors,
		)
		mux.Handle(deliveryPath, deliveryHandler)
	}

	return mux, nil
}
