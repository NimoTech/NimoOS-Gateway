# CasaOS-Gateway 详解

CasaOS-Gateway 是系统的 API 网关，所有外部 HTTP 请求的唯一入口，负责动态路由、反向代理和静态资源服务。

---

## 核心职责

- 作为反向代理，将外部请求路由到对应的微服务
- 动态路由管理（服务启动时自动注册/注销路由）
- 静态前端资源服务
- API 聚合（将多个微服务的 API 汇聚到单一端口 8080）
- JWT 认证（管理接口）
- 零停机端口切换

---

## 目录结构

```
CasaOS-Gateway/
├── main.go                  # 启动入口（Uber FX 依赖注入）
├── service/
│   ├── management.go        # 路由管理（注册、查找、持久化）
│   └── state.go             # 应用状态（网关端口、运行时路径）
├── route/
│   ├── management_route.go  # 管理 API（/v1/gateway/*）
│   ├── gateway_route.go     # 反向代理路由（主入口）
│   └── static_route.go      # 静态资源服务
├── common/                  # 配置加载
├── pkg/                     # 端口工具
└── build/                   # systemd 服务文件
```

---

## 三层服务架构

| 服务 | 职责 | 监听 |
|---|---|---|
| Gateway Server | 外部流量反向代理 | 配置端口（默认 80 或 8080） |
| Management Server | 路由注册管理 API | 随机端口（写入 management.url） |
| Static Server | 前端静态资源 | 随机端口（写入 static.url） |

---

## 路由工作原理

1. 其他服务启动后通过 `POST /v1/gateway/routes` 注册路由
2. 网关维护 `path → 反向代理` 映射表（内存 + JSON 持久化）
3. 外部请求到达时，按路径最长前缀匹配找到对应代理
4. 使用 Go 标准库 `httputil.ReverseProxy` 转发请求

**路径持久化**：路由保存在 `/var/run/casaos/routes.json`，重启后自动恢复。

---

## 管理 API

| 方法 | 路径 | 说明 | 认证 |
|---|---|---|---|
| GET | `/v1/gateway/routes` | 列出所有路由 | 无 |
| POST | `/v1/gateway/routes` | 注册新路由 | JWT 或 localhost |
| GET | `/v1/gateway/port` | 查询当前端口 | 无 |
| PUT | `/v1/gateway/port` | 修改网关端口 | JWT 或 localhost |
| GET | `/ping` | 健康检查 | 无 |

---

## 安全机制

- **JWT 验证**：ECDSA 公钥，来自 CasaOS-UserService
- **Localhost 免验证**：来自 127.0.0.1 / ::1 的请求跳过 JWT 校验
- **IP 防伪造**：校验 `X-Forwarded-For` 头，防止攻击者注入 IP

---

## 零停机端口切换

1. 在新端口创建新 HTTP Server
2. 发送 `/ping` 验证新 Server 就绪
3. 等待 1 秒宽限期后优雅关闭旧 Server
4. 全程无请求丢失

---

## 运行时文件

| 文件 | 内容 |
|---|---|
| `/var/run/casaos/management.url` | 管理服务地址 |
| `/var/run/casaos/gateway.url` | 网关服务地址 |
| `/var/run/casaos/static.url` | 静态资源服务地址 |
| `/var/run/casaos/routes.json` | 持久化路由表 |
| `/var/run/casaos/casaos.pub` | JWT ECDSA 公钥 |

---

## 配置

```ini
[common]
RuntimePath = /var/run/casaos

[gateway]
port =                 # 留空自动探测（优先 80，备选 8080）
```

---

## 技术栈

- **框架**：Echo v4
- **依赖注入**：Uber FX
- **反向代理**：Go 标准库 `net/http/httputil`
- **systemd 集成**：coreos/go-systemd
