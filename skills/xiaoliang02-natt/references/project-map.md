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
- `nattserver/internal/control`: dual-stack control/data runtime, TCP and UDP tunnel lifecycle, heartbeats, tunnel occupancy, `tunnel_stop`, UDP public-session relay.
- `nattserver/internal/protocol`: protocol message types and frame read/write.
- `nattserver/internal/db`: SQLite migration and repositories.
- `nattserver/internal/control/tunnel_state.go`: instance-owned server tunnel-state cache, immediate critical state flushes, 30-second heartbeat batching, and two-minute offline retention.
- `nattserver/internal/auth`: JWT, SM2, SM3 password/secret hashing, secret generation.
- `nattserver/internal/mcp`: JSON-RPC Streamable HTTP endpoint and server tools.
- `nattserver/Web/EmbedFiles`: embedded HTML/CSS/JS/Layui frontend.

## Client Module Map

- `nattuser/main.go`: startup, config loading, initialization mode, Web runner, control manager runner.
- `nattuser/internal/config`: default config, JSON/YAML parsing, validation.
- `nattuser/internal/startup`: first-run initialization Web and runtime directory creation.
- `nattuser/internal/api`: Gin REST routes, frontend routes, auth, tunnel connections, local tunnels, config, audit, MCP config.
- `nattuser/internal/control`: server control connection, reconnect, TCP data binding, UDP relay/local UDP sessions, `tunnel_stop` handling.
- `nattuser/internal/protocol`: protocol message types and frame read/write.
- `nattuser/internal/db`: SQLite migration and repositories.
- `nattuser/internal/control/connection_state.go`: instance-owned client connection-state cache, live list overlays, critical state persistence, and bounded offline retention.
- `nattuser/internal/auth`: JWT, SM2, SM3 password hashing, secret helpers.
- `nattuser/internal/mcp`: JSON-RPC Streamable HTTP endpoint and client tools.
- `nattuser/Web/EmbedFiles`: embedded HTML/CSS/JS/Layui frontend.

## Current Public Behavior

- Server Web brand: `小良内网穿透-服务端`.
- Client Web brand: `小良内网穿透-客户端`.
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
- Tunnel protocol is persisted as `tcp` or `udp`; server/client list endpoints accept `protocol=tcp|udp`, and the old TCP pages always request `protocol=tcp`.
- Server uniqueness is `(protocol, remote_port)`, allowing one TCP and one UDP listener to share a port number.
- Listeners use IPv4/IPv6 dual stack. Configuration defaults to `[::]`; the runtime creates separate IPv4 and IPv6 listeners, and an IPv6 bind failure logs a Chinese warning without disabling IPv4. Legacy `0.0.0.0` configuration remains compatible.
- Starting a server tunnel while its client is offline should move it to `waiting`, set `auto_start=true`, clear `last_error`, and avoid binding the public port until reconnect.
- If a running tunnel loses its control connection, the server writes `last_error=隧道控制连接已离线`; only this error is automatically recovered when the same client reconnects or sends heartbeat.
- UDP relay recovery is also triggered by client authentication or heartbeat after a relay disconnect or process restart. Recoverable UDP states are `waiting`, `running`, and `last_error=UDP中继连接已离线`; the client rebinds the data channel and the server marks it running only after `udp_bind_ack`. Manual `stopped` and unrelated errors do not recover automatically.
- `heartbeat_ack` carries `tunnel_status`, `last_error`, and `remote_port`; the client uses it as a fallback to sync server `stopped`/`error` when it misses a command.
- Client Web/MCP “disconnect” is user intent: it sets the connection to `stopped`, clears `last_error`, and disables `auto_start`; Web/MCP “connect” enables `auto_start` again.
- If the server tunnel is stopped, the client status detail must be:

```text
服务端暂停了隧道连接，请通知服务端人员启动隧道。
```

- If a tunnel is occupied by another client, the visible status detail should be:

```text
该连接正在占用，不得连接
```

### UDP Relay Rules

- Server UDP runtime is `nattserver/internal/control/udp.go`; client UDP runtime is `nattuser/internal/control/udp.go`.
- The client opens a long-lived TCP data channel, sends `udp_bind`, waits for `udp_bind_ack`, then receives and sends `udp_packet` frames.
- Each public UDP source address is a session. Both sides expire idle sessions after 60 seconds by default; a tunnel permits at most 1024 sessions and a packet may not exceed 65507 bytes.
- Stopping/deleting a tunnel or losing the relay closes listeners and session mappings. A lost relay writes a Chinese UDP relay offline status detail; the client reconnects the relay while control remains online.

## Web UI Notes

- Frontend files are embedded under `Web/EmbedFiles`.
- Pages are split into `login.html`, `index.html`, `dashboard.html`, `tunnels.html` (TCP), `udp-tunnels.html` (UDP), `config.html`, `audit.html`, and `mcp.html`.
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

`client.create_tunnel_connection` requires `name`, `server_host`, `client_secret`, `local_host`, and `local_port`. Its optional `protocol` is `tcp` or `udp` and defaults to `tcp`. Omitted or zero `server_port` / `data_port` use the client's `server_defaults` config. `server.create_tunnel` also accepts `protocol` and defaults to `tcp`.

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
