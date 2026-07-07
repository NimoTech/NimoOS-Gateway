# NimoOS-Gateway 详解

NimoOS-Gateway 是系统的 API 网关，所有外部 HTTP 请求的唯一入口，负责动态路由、反向代理、静态资源服务，并支持 HTTPS（自签 CA 或自定义证书）。

---

## 核心职责

- 作为反向代理，将外部请求路由到对应的微服务
- 动态路由管理（服务启动时自动注册/注销路由）
- 静态前端资源服务
- API 聚合（将多个微服务的 API 汇聚到单一入口端口，默认 80）
- HTTPS 网关（可选，自签 CA 自动生成或用户上传证书，见 `service/management.go`）
- 统一 CORS 处理（含 tus 断点续传协议头，见 `route/gateway_route.go`）
- JWT 认证（管理接口）
- 零停机端口切换 / SSL 配置热重载

---

## 目录结构

```
NimoOS-Gateway/
├── main.go                  # 启动入口（Uber FX）+ HTTP/HTTPS 网关重载逻辑
├── service/
│   ├── management.go        # 路由管理（注册、查找、持久化）+ 自签证书生成
│   └── state.go             # 应用状态（网关端口、SSL 配置、运行时路径）
├── route/
│   ├── management_route.go  # 管理 API（/v1/gateway/*，含 SSL 配置/证书上传）
│   ├── gateway_route.go     # 反向代理路由（主入口，CORS + /_internal/ 拦截）
│   └── static_route.go      # 静态资源服务
├── api/gateway/openapi.yaml # OpenAPI spec
├── cmd/migration-tool/      # 迁移工具
├── common/                  # 配置加载（common/config.go）、版本号
├── pkg/                     # 端口工具
└── build/                   # systemd 服务文件、配置样例（gateway.ini.sample）
```

---

## 三层服务架构

| 服务 | 职责 | 监听 |
|---|---|---|
| Gateway Server | 外部流量反向代理 | HTTP 配置端口（默认自动探测 80–89、8080–8089）；可选 HTTPS 端口（默认 443） |
| Management Server | 路由注册管理 API | localhost 随机端口（写入 management.url） |
| Static Server | 前端静态资源 | localhost 随机端口（写入 static.url） |

---

## 路由工作原理

1. 其他服务启动后通过 `POST /v1/gateway/routes` 注册路由
2. 网关维护 `path → 反向代理` 映射表（内存 + JSON 持久化，`service/management.go`）
3. 外部请求到达时，按路径最长前缀匹配找到对应代理
4. 使用 Go 标准库 `httputil.ReverseProxy` 转发请求

**大文件上传优化**：所有反向代理共用自定义 Transport（256KB 读写缓冲、300s 响应头超时、连接池复用）并设 `FlushInterval = -1` 即时流式转发；网关 Server 的 `ReadHeaderTimeout` 为 30s（`service/management.go` 的 `uploadTransport`、`main.go`）。

**路径持久化**：路由保存在 `/var/run/nimoos/routes.json`，重启后自动恢复。

---

## 管理 API

| 方法 | 路径 | 说明 | 认证 |
|---|---|---|---|
| GET | `/v1/gateway/routes` | 列出所有路由 | 无 |
| POST | `/v1/gateway/routes` | 注册新路由 | JWT 或 localhost |
| GET | `/v1/gateway/port` | 查询当前端口 | 无 |
| PUT | `/v1/gateway/port` | 修改网关端口 | JWT 或 localhost |
| GET | `/v1/gateway/ssl` | 查询 SSL 配置（含证书生效/过期时间） | 无 |
| PUT | `/v1/gateway/ssl` | 修改 SSL 配置（开关/端口/域名/证书类型） | JWT 或 localhost |
| POST | `/v1/gateway/ssl/upload` | 上传自定义证书（multipart：`crt` + `pem`/`key`，校验密钥对有效性） | JWT 或 localhost |
| GET | `/v1/gateway/ssl/ca` | 下载自签根 CA 证书（`nimoos-ca.crt`，供客户端信任） | 无 |
| GET | `/ping` | 健康检查 | 无 |

> `/v1/gateway/port`、`/v1/gateway/ssl*` 由网关启动时向自身注册路由指向 Management Server（`main.go`）。

---

## HTTPS / SSL

- **配置项**（`common/config.go`）：`gateway.SSLEnabled`（默认 false）、`SSLPort`（默认 443）、`SSLDomain`（默认 nimoos.local）、`SSLCertType`（`auto` / `custom`）
- **auto 模式**：自动生成自签根 CA（`NimoOS-CA`，ECDSA P-256，10 年有效期）并用其签发服务器证书，SAN 覆盖配置域名/IP、`localhost`、回环地址及本机所有网卡 IP（`service/management.go` 的 `GenerateSelfSignedCert`）；证书缺失、过期或 CommonName 与配置域名不符时自动重新生成
- **custom 模式**：经 `/v1/gateway/ssl/upload` 上传证书/私钥，`tls.LoadX509KeyPair` 校验失败则回滚删除
- **证书存放**：`/etc/nimoos/certs/`（`ca.crt` / `ca.key` / `gateway.crt` / `gateway.key`）
- HTTP 与 HTTPS 网关并存，各自独立零停机重载（`main.go` 的 `reloadGateways`）；SSL 配置变更实时写回 `gateway.ini`

---

## 安全机制

- **JWT 验证**：ECDSA 公钥经 NimoOS-UserService 的 JWKS 端点获取（通过 `user-service.url` 发现，见 NimoOS-Common `external.GetPublicKey`）
- **Localhost 免验证**：来自 127.0.0.1 / ::1 的请求跳过 JWT 校验
- **IP 防伪造**：校验并重写 `X-Forwarded-For` / `X-Real-IP` 头，防止攻击者注入 IP（`route/gateway_route.go` 的 `rewriteRequestSourceIP`）
- **内部端点拦截**：路径含 `/_internal/` 的请求一律不代理、直接返回 404（`route/gateway_route.go`）。下游服务（如 NimoOS-AI）在 `/_internal/` 下挂载仅靠 LocalhostOnly 保护的敏感接口，而网关从回环转发会使该检查对外部调用者失效，故在网关层整类封禁
- **CORS**：网关统一下发 CORS 响应头并直接应答 OPTIONS 预检；Allow/Expose 头覆盖 tus 断点续传协议（`Tus-Resumable`、`Upload-Offset`、`Location` 等），保证跨源续传可用

---

## 零停机端口切换

1. 在新端口创建新 HTTP(S) Server
2. 请求 `/ping` 验证新 Server 就绪（HTTPS 用跳过证书校验的客户端探测）
3. 等待 1 秒宽限期后优雅关闭旧 Server
4. 全程无请求丢失

---

## 运行时文件

| 文件 | 内容 |
|---|---|
| `/var/run/nimoos/management.url` | 管理服务地址 |
| `/var/run/nimoos/static.url` | 静态资源服务地址 |
| `/var/run/nimoos/routes.json` | 持久化路由表 |
| `/var/run/nimoos/gateway.pid` | 网关进程 PID |

---

## 配置

```ini
[common]
runtimepath = /var/run/nimoos

[gateway]
port =                 # 留空自动探测（80–89，备选 8080–8089）
sslenabled = false     # HTTPS 开关
sslport = 443
ssldomain = nimoos.local
sslcerttype = auto     # auto（自签）/ custom（上传）
```

---

## 技术栈

- **框架**：Echo v4（管理/静态服务）+ 标准库 `net/http`（网关主入口）
- **依赖注入**：Uber FX
- **反向代理**：Go 标准库 `net/http/httputil`
- **证书**：标准库 `crypto/x509` + ECDSA P-256 自签链
- **systemd 集成**：coreos/go-systemd
