# xiaoliang02 NATT Project Map

## Repository Layout

```text
D:\2025Code\ai\natt
  nattserver\    Public server, Web console, public listeners, client auth, tunnel runtime
  nattuser\      Intranet client, Web console, server connection config, local forwarding
  skills\        AI-facing project skills
```

## Server Module Map

- `nattserver/main.go`: startup, config loading, initialization mode, port preflight, service runners.
- `nattserver/internal/config`: default config, JSON/YAML parsing, validation.
- `nattserver/internal/startup`: first-run initialization Web, HTTPS cert generation, port checks.
- `nattserver/internal/api`: Gin REST routes, frontend routes, auth, clients, tunnels, config, audit, MCP config.
- `nattserver/internal/control`: control/data TCP runtime, tunnel lifecycle, heartbeats, tunnel occupancy, `tunnel_stop`.
- `nattserver/internal/protocol`: protocol message types and frame read/write.
- `nattserver/internal/db`: SQLite migration and repositories.
- `nattserver/internal/auth`: JWT, SM2, SM3 password/secret hashing, secret generation.
- `nattserver/internal/mcp`: JSON-RPC Streamable HTTP endpoint and server tools.
- `nattserver/Web/EmbedFiles`: embedded HTML/CSS/JS/Layui frontend.

## Client Module Map

- `nattuser/main.go`: startup, config loading, initialization mode, Web runner, control manager runner.
- `nattuser/internal/config`: default config, JSON/YAML parsing, validation.
- `nattuser/internal/startup`: first-run initialization Web and runtime directory creation.
- `nattuser/internal/api`: Gin REST routes, frontend routes, auth, tunnel connections, local tunnels, config, audit, MCP config.
- `nattuser/internal/control`: server control connection, reconnect, data bind, local TCP forwarding, `tunnel_stop` handling.
- `nattuser/internal/protocol`: protocol message types and frame read/write.
- `nattuser/internal/db`: SQLite migration and repositories.
- `nattuser/internal/auth`: JWT, SM2, SM3 password hashing, secret helpers.
- `nattuser/internal/mcp`: JSON-RPC Streamable HTTP endpoint and client tools.
- `nattuser/Web/EmbedFiles`: embedded HTML/CSS/JS/Layui frontend.

## Current Public Behavior

- Server Web brand: `工具人小良-内网穿透服务端`.
- Client Web brand: `工具人小良-内网穿透客户端`.
- Default startup requires runtime-root config:
  - Server: `xiaoliang02_server/config/config.json`.
  - Client: `xiaoliang02_user/config/config.json`.
- If default config or admin user is missing, default startup enters `init.html`.
- Explicit `-config` skips interactive fallback expectations and reads the provided path.
- JSON is the default config format; YAML remains supported only when explicitly passed.

## Tunnel Rules

- Server owns public listener fields: remote host, remote port, status, auto start, secret, traffic.
- Client owns local target fields: server host, control port, data port, server tunnel secret, local host, local port.
- One server tunnel ID may be occupied by only one active client connection at a time.
- If the server tunnel is stopped, the client status detail must be:

```text
服务端暂停了隧道连接，请通知服务端人员启动隧道。
```

- If a tunnel is occupied by another client, the visible status detail should be:

```text
该连接正在占用，不得连接
```

## Web UI Notes

- Frontend files are embedded under `Web/EmbedFiles`.
- Pages are split into `login.html`, `index.html`, `dashboard.html`, `tunnels.html`, `config.html`, `audit.html`, and `mcp.html`.
- The active menu is stored in `sessionStorage`.
- Long status details are truncated in tables and shown fully in a popup.
- Secrets are masked by default and revealed by a button.

## MCP Notes

Endpoint:

```text
POST /mcp
```

Auth headers:

```text
Authorization: Bearer <MCP_KEY>
X-MCP-Token: <MCP_KEY>
```

Methods:

- `initialize`
- `notifications/initialized`
- `ping`
- `tools/list`
- `tools/call`

Server tools:

- `server.list_clients`
- `server.get_client`
- `server.list_tunnels`
- `server.create_tunnel` (omitted `auto_start` defaults to `true`, initial status `waiting`)
- `server.start_tunnel`
- `server.stop_tunnel`
- `server.delete_tunnel`
- `server.get_dashboard`

Client tools:

- `client.list_tunnel_connections`
- `client.list_servers`
- `client.create_tunnel_connection`
- `client.delete_tunnel_connection`
- `client.connect_tunnel`
- `client.connect_server`
- `client.disconnect_tunnel`
- `client.disconnect_server`
- `client.list_tunnels`
- `client.get_network_status`

`client.create_tunnel_connection` requires `name`, `server_host`, `client_secret`, `local_host`, and `local_port`. Omitted or zero `server_port` / `data_port` use the client's `server_defaults` config.

## Troubleshooting

- If `rg` cannot run on Windows, use:

```powershell
Get-ChildItem -Recurse -File | Select-String -Pattern 'text'
```

- If Go tests fail because a temp exe is locked, set a unique `GOTMPDIR` under the workspace.
- If an exe starts in an empty folder, browse to the default initialization URL for that side.
- If production login fails with encrypted password errors, inspect SM2 public key loading and the embedded `sm2.js`.
- If MCP is not discoverable, confirm `/mcp` is enabled, token is correct, and the caller is not using the old route.

## Fallback Source

When local files, project docs, and this skill are insufficient, inspect the upstream project at:

```text
https://gitcode.com/gongjuliang/xiaoliang02
```
