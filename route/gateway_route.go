package route

import (
	"net/http"
	"strings"

	"github.com/NimoTech/NimoOS-Common/utils/logger"
	"github.com/NimoTech/NimoOS-Gateway/service"
	"go.uber.org/zap"
)

type GatewayRoute struct {
	management *service.Management
}

func NewGatewayRoute(management *service.Management) *GatewayRoute {
	return &GatewayRoute{
		management: management,
	}
}

// the function is to ensure the request source IP is correct.
func rewriteRequestSourceIP(r *http.Request) {
	// we may receive two kinds of requests. a request from reverse proxy. a request from client.

	// in reverse proxy, X-Forwarded-For will like
	// `X-Forwarded-For:[192.168.6.102]`(normal)
	// `X-Forwarded-For:[::1, 192.168.6.102]`(hacked) Note: the ::1 is inject by attacker.
	// `X-Forwarded-For:[::1]`(normal or hacked) local request. But it from browser have JWT. So we can and need to verify it
	// `X-Forwarded-For:[::1,::1]`(normal or hacked) attacker can build the request to bypass the verification.
	// But in the case. the remoteAddress should be the real ip. So we can use remoteAddress to verify it.

	ipList := []string{}

	// when r.Header.Get("X-Forwarded-For") is "". the ipList should be empty.
	// fix https://github.com/NimoTech/NimoOS/issues/1247
	if r.Header.Get("X-Forwarded-For") != "" {
		ipList = strings.Split(r.Header.Get("X-Forwarded-For"), ",")

		// when r.Header.Get("X-Forwarded-For") is "". to clean the ipList.
		// fix https://github.com/NimoTech/NimoOS/issues/1247
		if len(ipList) == 1 && ipList[0] == "" {
			ipList = []string{}
		}
	}

	r.Header.Del("X-Forwarded-For")
	r.Header.Del("X-Real-IP")

	// Note: the X-Forwarded-For depend the correct config from reverse proxy.
	// otherwise the X-Forwarded-For may be empty.
	remoteIP := r.RemoteAddr[:strings.LastIndex(r.RemoteAddr, ":")]
	if len(ipList) > 0 && (remoteIP == "127.0.0.1" || remoteIP == "::1") {
		// to process the request from reverse proxy

		// in reverse proxy, X-Forwarded-For will container multiple IPs.
		// if the request is from reverse proxy, the r.RemoteAddr will be 127.0.0.1.
		// So we need get ip from X-Forwarded-For
		r.Header.Add("X-Forwarded-For", ipList[len(ipList)-1])
	}
	// to process the request from client.
	// the gateway will add the X-Forwarded-For to request header.
	// So we didn't need to add it.
}

func (g *GatewayRoute) GetRoute() *http.ServeMux {
	gatewayMux := http.NewServeMux()
	gatewayMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH, HEAD")
		// tus resumable upload needs its protocol request headers allowed, and its
		// response headers exposed so tus-js-client can read the upload URL (Location)
		// and resume offset (Upload-Offset) cross-origin. Without these, cross-origin
		// tus uploads fail the preflight or cannot resume.
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Tus-Resumable, Upload-Length, Upload-Offset, Upload-Metadata, Upload-Concat, Upload-Defer-Length, X-HTTP-Method-Override")
		w.Header().Set("Access-Control-Expose-Headers", "Location, Upload-Offset, Upload-Length, Tus-Resumable, Tus-Version, Tus-Extension, Tus-Max-Size, Upload-Metadata, Upload-Concat, Upload-Defer-Length")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.URL.Path == "/ping" {
			w.WriteHeader(http.StatusOK)
			if _, err := w.Write([]byte("pong from gateway service")); err != nil {
				logger.Error("Failed to `pong` in resposne to `ping`", zap.Any("error", err))
			}
			return
		}

		// Never proxy internal endpoints. Downstream services mount
		// secret-returning handlers under `/_internal/` guarded only by a
		// LocalhostOnly check; because the gateway forwards from loopback,
		// that check would be satisfied for any external caller. Refuse the
		// whole class here — no service registers a public `_internal` route.
		// 404 (not 403) matches the not-found branch below and avoids
		// confirming the endpoint exists.
		if strings.Contains(r.URL.Path, "/_internal/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// Component /version endpoints are for the Gateway's internal server-side
		// probing only. Refuse external proxying so callers can't scan each
		// service's precise version (a CVE-targeting information leak). Internal
		// probes hit service targets directly, not through this proxy.
		// /v1/sys/version is an exception: it's a pre-existing, unrelated
		// endpoint (the UI's app-update-check) that must stay externally
		// reachable, even though it also ends in "/version".
		if strings.HasSuffix(r.URL.Path, "/version") && r.URL.Path != "/v1/sys/version" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		proxy := g.management.GetProxy(r.URL.Path)

		if proxy == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// to fix https://github.com/NimoTech/NimoOS/security/advisories/GHSA-32h8-rgcj-2g3c#event-102885
		// API V1 and V2 both read ip from request header. So the fix is effective for v1 and v2.
		rewriteRequestSourceIP(r)

		proxy.ServeHTTP(w, r)
	})

	return gatewayMux
}
