# NimoOS-Gateway in Detail

NimoOS-Gateway is the system's API gateway — the single entry point for all external HTTP requests. It handles dynamic routing, reverse proxying, static asset serving, and supports HTTPS (self-signed CA or a custom certificate).

---

## Core Responsibilities

- Act as a reverse proxy, routing external requests to the right microservice
- Manage dynamic routes (services auto-register/unregister routes on startup)
- Serve static frontend assets
- API aggregation (bring every microservice's API together behind a single entry port, default 80)
- HTTPS gateway (optional; self-signed CA auto-generated, or a user-uploaded certificate — see `service/management.go`)
- Unified CORS handling (including tus resumable-upload protocol headers, see `route/gateway_route.go`)
- JWT authentication (for the management API)
- Zero-downtime port switching / hot-reloading SSL config

---

## Directory Layout

```
NimoOS-Gateway/
├── main.go                  # entry point (Uber FX) + HTTP/HTTPS gateway reload logic
├── service/
│   ├── management.go        # route management (register, lookup, persistence) + self-signed cert generation
│   └── state.go             # app state (gateway port, SSL config, runtime paths)
├── route/
│   ├── management_route.go  # management API (/v1/gateway/*, incl. SSL config/cert upload)
│   ├── gateway_route.go     # reverse-proxy routes (main entry point, CORS + /_internal/ interception)
│   └── static_route.go      # static asset serving
├── api/gateway/openapi.yaml # OpenAPI spec
├── cmd/migration-tool/      # migration tool
├── common/                  # config loading (common/config.go), version number
├── pkg/                     # port utilities
└── build/                   # systemd unit file, sample config (gateway.ini.sample)
```

---

## Three-Tier Service Architecture

| Server | Responsibility | Listens on |
|---|---|---|
| Gateway Server | reverse-proxies external traffic | configured HTTP port (auto-probes 80–89, 8080–8089 by default); optional HTTPS port (default 443) |
| Management Server | route registration management API | localhost, random port (written to management.url) |
| Static Server | frontend static assets | localhost, random port (written to static.url) |

---

## How Routing Works

1. Other services register their routes via `POST /v1/gateway/routes` at startup
2. The gateway maintains a `path → reverse proxy` map (in memory + JSON-persisted, `service/management.go`)
3. Incoming requests are matched to a proxy by longest-prefix path match
4. Requests are forwarded using the Go standard library's `httputil.ReverseProxy`

**Large upload optimization**: every reverse proxy shares a custom Transport (256KB read/write buffers, 300s response-header timeout, connection pool reuse) with `FlushInterval = -1` for immediate stream forwarding; the gateway Server's `ReadHeaderTimeout` is 30s (`uploadTransport` in `service/management.go`, and `main.go`).

**Route persistence**: routes are saved to `/var/run/nimoos/routes.json` and restored automatically on restart.

---

## Management API

| Method | Path | Description | Auth |
|---|---|---|---|
| GET | `/v1/gateway/routes` | list all routes | none |
| POST | `/v1/gateway/routes` | register a new route | JWT or localhost |
| GET | `/v1/gateway/port` | query the current port | none |
| PUT | `/v1/gateway/port` | change the gateway port | JWT or localhost |
| GET | `/v1/gateway/ssl` | query SSL config (incl. certificate validity/expiry) | none |
| PUT | `/v1/gateway/ssl` | change SSL config (toggle/port/domain/cert type) | JWT or localhost |
| POST | `/v1/gateway/ssl/upload` | upload a custom certificate (multipart: `crt` + `pem`/`key`; validates the key pair) | JWT or localhost |
| GET | `/v1/gateway/ssl/ca` | download the self-signed root CA certificate (`nimoos-ca.crt`, for clients to trust) | none |
| GET | `/ping` | health check | none |

> `/v1/gateway/port` and `/v1/gateway/ssl*` are registered by the gateway against itself at startup, pointing to the Management Server (`main.go`).

---

## HTTPS / SSL

- **Config keys** (`common/config.go`): `gateway.SSLEnabled` (default false), `SSLPort` (default 443), `SSLDomain` (default nimoos.local), `SSLCertType` (`auto` / `custom`)
- **auto mode**: generates a self-signed root CA (`NimoOS-CA`, ECDSA P-256, 10-year validity) and uses it to issue the server certificate; the SAN covers the configured domain/IP, `localhost`, loopback addresses, and every local NIC's IP (`GenerateSelfSignedCert` in `service/management.go`); the certificate is regenerated automatically if missing, expired, or its CommonName no longer matches the configured domain
- **custom mode**: certificate/key are uploaded via `/v1/gateway/ssl/upload`; if `tls.LoadX509KeyPair` fails validation, the upload is rolled back and deleted
- **Certificate storage**: `/etc/nimoos/certs/` (`ca.crt` / `ca.key` / `gateway.crt` / `gateway.key`)
- HTTP and HTTPS gateways run side by side, each reloading independently with zero downtime (`reloadGateways` in `main.go`); SSL config changes are written back to `gateway.ini` immediately

---

## Security

- **JWT verification**: ECDSA public key fetched from NimoOS-UserService's JWKS endpoint (discovered via `user-service.url`; see `external.GetPublicKey` in NimoOS-Common)
- **Localhost bypass**: requests from 127.0.0.1 / ::1 skip JWT verification
- **IP spoofing protection**: `X-Forwarded-For` / `X-Real-IP` headers are validated and rewritten to stop attackers from injecting a fake IP (`rewriteRequestSourceIP` in `route/gateway_route.go`)
- **Internal endpoint blocking**: any request whose path contains `/_internal/` is never proxied — it gets an immediate 404 (`route/gateway_route.go`). Downstream services (e.g. NimoOS-AI) mount sensitive endpoints under `/_internal/` protected only by a LocalhostOnly check; since gateway-forwarded requests originate from loopback, that check alone wouldn't stop external callers, so the whole path prefix is blocked at the gateway layer instead
- **CORS**: the gateway centrally issues CORS response headers and answers OPTIONS preflight directly; the Allow/Expose headers cover the tus resumable-upload protocol (`Tus-Resumable`, `Upload-Offset`, `Location`, etc.) so cross-origin resumable uploads keep working

---

## Zero-Downtime Port Switching

1. A new HTTP(S) Server is created on the new port
2. `/ping` is probed to confirm the new Server is ready (HTTPS uses a client that skips certificate verification for the probe)
3. After a 1-second grace period, the old Server is shut down gracefully
4. No requests are dropped throughout

---

## Runtime Files

| File | Contents |
|---|---|
| `/var/run/nimoos/management.url` | Management Server address |
| `/var/run/nimoos/static.url` | Static Server address |
| `/var/run/nimoos/routes.json` | persisted route table |
| `/var/run/nimoos/gateway.pid` | gateway process PID |

---

## Configuration

```ini
[common]
runtimepath = /var/run/nimoos

[gateway]
port =                 # blank = auto-probe (80–89, falling back to 8080–8089)
sslenabled = false     # HTTPS toggle
sslport = 443
ssldomain = nimoos.local
sslcerttype = auto     # auto (self-signed) / custom (uploaded)
```

---

## Tech Stack

- **Framework**: Echo v4 (management/static servers) + standard library `net/http` (main gateway entry)
- **Dependency injection**: Uber FX
- **Reverse proxy**: Go standard library `net/http/httputil`
- **Certificates**: standard library `crypto/x509` + a self-signed ECDSA P-256 chain
- **systemd integration**: coreos/go-systemd
