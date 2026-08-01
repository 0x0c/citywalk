// Package connectserver assembles the Connect handlers the phase-one single process serves (CW-0010
// Unit 2, Unit 11) into one HTTP mux. Later passes register the definition, audience, and delivery
// services' handlers alongside HealthServer here.
package connectserver

import (
	"net/http"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"

	"github.com/0x0c/citywalk/gen/citywalk/platform/v1/platformv1connect"
)

// NewMux builds the HTTP handler serving every Connect service the process hosts, instrumented with
// OpenTelemetry so every request carries the trace and metric signals CW-0010 Unit 10 requires.
func NewMux() (http.Handler, error) {
	otelInterceptor, err := otelconnect.NewInterceptor()
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	path, handler := platformv1connect.NewHealthServiceHandler(
		HealthServer{},
		connect.WithInterceptors(otelInterceptor),
	)
	mux.Handle(path, handler)
	return mux, nil
}
