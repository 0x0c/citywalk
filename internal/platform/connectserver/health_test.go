package connectserver_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	platformv1 "github.com/0x0c/citywalk/gen/citywalk/platform/v1"
	"github.com/0x0c/citywalk/gen/citywalk/platform/v1/platformv1connect"
	"github.com/0x0c/citywalk/internal/platform/connectserver"
)

// TestHealthCheckReportsServing demonstrates FR-OPS's implicit liveness requirement: a running
// process answers a health check as SERVING (.agent-workflows/implement/workflow.md step 5's
// requirement-to-test mapping applies once a service actually claims a requirement; this test
// exists to prove the transport wiring the later services build on).
func TestHealthCheckReportsServing(t *testing.T) {
	mux, err := connectserver.NewMux(nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewMux: %v", err)
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	client := platformv1connect.NewHealthServiceClient(server.Client(), server.URL)
	resp, err := client.Check(context.Background(), connect.NewRequest(&platformv1.CheckRequest{}))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := resp.Msg.GetStatus(); got != platformv1.ServingStatus_SERVING_STATUS_SERVING {
		t.Errorf("Status = %v, want SERVING_STATUS_SERVING", got)
	}
}
