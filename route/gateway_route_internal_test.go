package route

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/NimoTech/NimoOS-Common/model"
	"github.com/NimoTech/NimoOS-Gateway/service"
	"gotest.tools/v3/assert"
)

// setupGateway builds a real GatewayRoute mux with one upstream route pointing
// at a live test server, so we can distinguish "denied by the gateway" (404
// before proxying) from "proxied to upstream" (reaches the test server).
func setupGateway(t *testing.T) (http.Handler, func()) {
	tmpdir, _ := os.MkdirTemp("", "nimoos-gateway-internal-test")

	state := service.NewState()
	if err := state.SetRuntimePath(tmpdir); err != nil {
		t.Fatal(err)
	}
	management := service.NewManagementService(state)

	upstream := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("upstream reached"))
		}))

	if err := management.CreateRoute(&model.Route{
		Path:   "/v1/ai",
		Target: upstream.URL,
	}); err != nil {
		t.Fatal(err)
	}

	mux := NewGatewayRoute(management).GetRoute()
	return mux, func() {
		upstream.Close()
		os.RemoveAll(tmpdir)
	}
}

func TestGatewayDeniesInternalPaths(t *testing.T) {
	mux, teardown := setupGateway(t)
	defer teardown()

	// Internal endpoints must never be reachable through the gateway, even
	// though they sit under a registered prefix (/v1/ai). They are guarded
	// downstream only by LocalhostOnly, which the gateway's loopback-origin
	// forward would otherwise satisfy.
	for _, p := range []string{
		"/v1/ai/_internal/agent/provider-credentials",
		"/v1/ai/_internal/mcp/runtime",
		"/v1/ai/_internal/chat/completions",
	} {
		req, _ := http.NewRequest(http.MethodGet, p, nil)
		req.RemoteAddr = "127.0.0.1:0"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		assert.Equal(t, http.StatusNotFound, w.Code)
		assert.Assert(t, w.Body.String() != "upstream reached")
	}
}

func TestGatewayForwardsNonInternalPaths(t *testing.T) {
	mux, teardown := setupGateway(t)
	defer teardown()

	// A normal path under the same prefix must still be proxied to upstream.
	req, _ := http.NewRequest(http.MethodGet, "/v1/ai/agent/sessions", nil)
	req.RemoteAddr = "127.0.0.1:0"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "upstream reached", w.Body.String())
}
