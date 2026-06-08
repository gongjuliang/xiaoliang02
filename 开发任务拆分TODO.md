# 内网穿透项目开发任务拆分 / TODO 清单

## 0. 开发约定

- [x] 服务端项目目录固定为 `nattserver`。
- [x] 客户端项目目录固定为 `nattuser`。
- [x] 首版仅实现 TCP 端口穿透，不实现 UDP、HTTP 域名转发、HTTPS 证书托管、P2P 打洞。
- [x] 服务端与客户端通信采用“控制连接 + 数据连接分离”模型。
- [x] TLS 做成可配置能力，开发环境允许关闭，生产环境建议开启。

## 1. 基础工程搭建

### 1.1 服务端 `nattserver`

- [x] 整理 Go module 名称和基础目录结构。
- [x] 引入 Gin、SQLite 驱动、日志、配置解析等基础依赖。
- [x] 在认证阶段引入 JWT、密码哈希等安全依赖。
- [x] 搭建 Gin HTTP 服务启动入口。
- [x] 实现配置文件加载，默认路径为 `nattserver/config/config.json`，并兼容旧 YAML。
- [x] 实现日志初始化，默认日志目录为 `nattserver/logs/`。
- [x] 实现 SQLite 初始化，默认数据库路径为 `nattserver/data/nattserver.db`。
- [x] 增加优雅退出逻辑，退出时关闭 HTTP 服务和数据库连接。
- [x] 在穿透协议阶段接入 TCP 监听器并纳入优雅退出。

### 1.2 客户端 `nattuser`

- [x] 整理 Go module 名称和基础目录结构。
- [x] 引入 Gin、SQLite 驱动、日志、配置解析等基础依赖。
- [x] 在认证阶段引入 JWT、密码哈希等安全依赖。
- [x] 搭建 Gin HTTP 服务启动入口。
- [x] 实现配置文件加载，默认路径为 `nattuser/config/config.json`，并兼容旧 YAML。
- [x] 实现日志初始化，默认日志目录为 `nattuser/logs/`。
- [x] 实现 SQLite 初始化，默认数据库路径为 `nattuser/data/nattuser.db`。
- [x] 增加优雅退出逻辑，退出时关闭 HTTP 服务和数据库连接。
- [x] 在控制连接阶段接入服务端连接并纳入优雅退出。

## 2. 数据库与基础模型

### 2.1 通用能力

- [x] 实现数据库自动建表或迁移机制。
- [x] 统一 `created_at`、`updated_at` 字段写入规则。
- [x] 统一状态枚举定义，避免字符串散落在业务代码中。
- [x] 封装分页查询结构。
- [x] 封装统一错误码和 API 响应结构。

### 2.2 服务端表

- [x] 实现 `users` 表。
- [x] 实现 `clients` 表。
- [x] 实现 `tunnels` 表。
- [x] 保留兼容用 `audit_logs` 表，并实现迁移到 JSONL 审计文件。
- [x] 实现 `settings` 表。
- [x] 实现 `traffic_stats` 表。
- [x] 为 `username`、`secret_hash`、`remote_port` 增加唯一约束。
- [x] 初始化默认管理员账号。

### 2.3 客户端表

- [x] 实现 `users` 表。
- [x] 实现 `server_connections` 表。
- [x] 实现 `local_tunnels` 表，用于绑定服务端隧道 ID 和本地 `local_host:local_port`。
- [x] 保留兼容用 `audit_logs` 表，并实现迁移到 JSONL 审计文件。
- [x] 实现 `settings` 表。
- [x] 初始化默认管理员账号。

## 3. Web 登录与安全基础

### 3.1 服务端后台认证

- [x] 实现 SM2 公钥获取接口。
- [x] 实现登录接口 `POST /api/server/v1/auth/login`。
- [x] 登录时解密前端 SM2 密文密码。
- [x] 使用 bcrypt 或 argon2 校验密码哈希。
- [x] 登录成功后签发 JWT。
- [x] 实现 JWT 刷新接口 `POST /api/server/v1/auth/refresh`。
- [x] 实现 Gin JWT 鉴权中间件。
- [x] 实现登录接口限流。
- [x] 登录、失败登录、刷新 token 写入审计日志。

### 3.2 客户端后台认证

- [x] 实现 SM2 公钥获取接口。
- [x] 实现登录接口 `POST /api/client/v1/auth/login`。
- [x] 登录时解密前端 SM2 密文密码。
- [x] 使用 bcrypt 或 argon2 校验密码哈希。
- [x] 登录成功后签发 JWT。
- [x] 实现 JWT 刷新接口 `POST /api/client/v1/auth/refresh`。
- [x] 实现 Gin JWT 鉴权中间件。
- [x] 实现登录接口限流。
- [x] 登录、失败登录、刷新 token 写入审计日志。

## 4. 服务端 Web API

### 4.1 客户端管理

- [x] 实现客户端列表接口 `GET /api/server/v1/clients`。
- [x] 实现新增客户端授权接口 `POST /api/server/v1/clients`。
- [x] 生成客户端秘钥，只在创建或轮换时返回明文。
- [x] 数据库存储客户端秘钥哈希和展示用摘要。
- [x] 实现编辑客户端接口 `PUT /api/server/v1/clients/:id`。
- [x] 实现启用客户端接口 `POST /api/server/v1/clients/:id/enable`。
- [x] 实现禁用客户端接口 `POST /api/server/v1/clients/:id/disable`。
- [x] 禁用客户端后断开其已有控制连接。
- [x] 实现轮换秘钥接口 `POST /api/server/v1/clients/:id/rotate-secret`。
- [x] 所有修改操作写入审计日志。

### 4.2 隧道管理

- [x] 实现隧道列表接口 `GET /api/server/v1/tunnels`。
- [x] 实现新增隧道接口 `POST /api/server/v1/tunnels`。
- [x] 服务端隧道创建/编辑不再接收 `local_host/local_port`，本地目标由客户端绑定。
- [x] 校验 `remote_port` 唯一且在允许端口范围内。
- [x] 实现编辑隧道接口 `PUT /api/server/v1/tunnels/:id`。
- [x] 实现删除隧道接口 `DELETE /api/server/v1/tunnels/:id`。
- [x] 删除运行中隧道前先停止监听并释放端口。
- [x] 实现启动隧道接口 `POST /api/server/v1/tunnels/:id/start`。
- [x] 实现停止隧道接口 `POST /api/server/v1/tunnels/:id/stop`。
- [x] 启停失败时记录 `last_error`。
- [x] 所有修改操作写入审计日志。

### 4.3 配置、仪表盘、日志

- [x] 实现仪表盘接口 `GET /api/server/v1/dashboard`。
- [x] 实现审计日志接口 `GET /api/server/v1/audit-logs`。
- [x] 实现配置查询接口 `GET /api/server/v1/config`。
- [x] 实现配置修改接口 `PUT /api/server/v1/config`。
- [x] 支持可热加载配置立即生效。
- [x] 不可热加载配置修改后提示需要重启。

## 5. 客户端 Web API

### 5.1 服务端连接管理

- [x] 实现服务端连接列表接口 `GET /api/client/v1/servers`。
- [x] 实现新增服务端连接接口 `POST /api/client/v1/servers`。
- [x] 实现编辑服务端连接接口 `PUT /api/client/v1/servers/:id`。
- [x] 实现删除服务端连接接口 `DELETE /api/client/v1/servers/:id`。
- [x] 实现连接服务端接口 `POST /api/client/v1/servers/:id/start`。
- [x] 实现断开服务端接口 `POST /api/client/v1/servers/:id/stop`。
- [x] 支持配置服务端地址、接入端口、客户端秘钥、自动连接。
- [x] 所有修改操作写入审计日志。

### 5.2 本地状态接口

- [x] 实现本地隧道状态接口 `GET /api/client/v1/tunnels`。
- [x] 实现本地隧道新增接口 `POST /api/client/v1/tunnels`。
- [x] 实现本地隧道编辑接口 `PUT /api/client/v1/tunnels/:id`。
- [x] 实现本地隧道删除接口 `DELETE /api/client/v1/tunnels/:id`。
- [x] 校验 `server_connection_id + server_tunnel_id` 唯一，并校验本地端口范围。
- [x] 实现本机运行状态接口 `GET /api/client/v1/status`。
- [x] 实现审计日志接口 `GET /api/client/v1/audit-logs`。
- [x] 实现配置查询接口 `GET /api/client/v1/config`。
- [x] 实现配置修改接口 `PUT /api/client/v1/config`。

## 6. 控制连接协议

### 6.1 协议基础

- [x] 定义控制消息结构：`type`、`request_id`、`client_id`、`tunnel_id`、`connection_id`、`timestamp`、`payload`。
- [x] 实现 4 字节大端长度前缀 + JSON 消息体的帧读写。
- [x] 定义协议错误结构和错误码。
- [x] 实现 request_id 生成和日志贯穿。
- [x] 为控制连接增加读写超时。

### 6.2 服务端控制连接

- [x] 启动客户端控制连接监听端口，默认 `25511`。
- [x] 接收 `auth_request` 并校验客户端秘钥。
- [x] 认证成功后绑定客户端在线状态。
- [x] 认证失败时返回错误并关闭连接。
- [x] 接收 `heartbeat` 并更新最后在线时间。
- [x] 连续 3 次心跳超时后标记客户端离线。
- [x] 客户端离线时停止或标记其隧道不可用。
- [x] 支持通过控制连接发送 `tunnel_start`、`tunnel_stop`、`data_open`、`data_close`。

### 6.3 客户端控制连接

- [x] 根据 `server_connections` 配置主动连接服务端。
- [x] 发送 `auth_request`。
- [x] 认证成功后保存连接状态。
- [x] 每 15 秒发送一次 `heartbeat`。
- [x] 断线后每 5 秒自动重连。
- [x] 接收隧道启停指令。
- [x] 接收 `data_open` 后建立数据连接。
- [x] 将连接状态、最近错误、最近心跳时间写入运行状态。

## 7. 数据连接与 TCP 转发

### 7.1 服务端隧道监听

- [x] 启动隧道时监听 `remote_host:remote_port`。
- [x] 监听失败时更新隧道状态为 `error` 并记录原因。
- [x] 接收公网访问连接时生成 `connection_id`。
- [x] 通过控制连接向客户端发送 `data_open`。
- [x] 等待客户端数据连接接入并绑定。
- [x] 绑定超时后关闭公网访问连接并记录错误。
- [x] 绑定成功后双向复制公网连接和数据连接流量。
- [x] 停止隧道时关闭监听器和所有活跃连接。

### 7.2 客户端数据连接

- [x] 收到 `data_open` 后连接服务端数据连接端口，默认 `25512`。
- [x] 数据连接首帧发送客户端秘钥、`tunnel_id`、`connection_id`。
- [x] 认证成功后按 `server_connection_id + server_tunnel_id` 查询 `local_tunnels`，再连接本地 `local_host:local_port`。
- [x] 缺少本地绑定或绑定禁用时向服务端返回 `data_close` 或隧道错误状态。
- [x] 本地服务连接失败时通知服务端关闭该连接。
- [x] 本地连接成功后双向复制本地连接和服务端数据连接流量。
- [x] 任一方向关闭时释放全部资源。

### 7.3 流量统计

- [x] 统计每条隧道当前连接数。
- [x] 统计每条隧道累计连接数。
- [x] 统计每条隧道上行字节数。
- [x] 统计每条隧道下行字节数。
- [x] 定期将统计数据写入 `traffic_stats`。
- [x] 仪表盘和 MCP 查询复用同一统计来源。

## 8. Web 前端页面

### 8.1 通用前端

- [x] 基于 HTML + CSS + jQuery + Layui 实现页面。
- [x] 封装 API 请求方法，统一携带 JWT。
- [x] 封装登录失效跳转。
- [x] 封装分页表格。
- [x] 封装确认弹窗和错误提示。

### 8.2 服务端页面

- [x] 登录页。
- [x] 仪表盘页。
- [x] 客户端管理页。
- [x] 隧道管理页。
- [x] 系统配置页。
- [x] 审计日志页。
- [x] 前端拆分为 `login.html`、`dashboard.html`、`clients.html`、`tunnels.html`、`config.html`、`audit.html` 等独立模块页。
- [x] 服务端隧道表单不再展示或提交本地地址/端口。
- [x] 隧道新增/编辑弹窗。
- [x] 客户端新增/编辑/秘钥轮换弹窗。

### 8.3 客户端页面

- [x] 登录页。
- [x] 服务端连接管理页。
- [x] 本地隧道状态页。
- [x] 本机状态页。
- [x] 系统配置页。
- [x] 审计日志页。
- [x] 前端拆分为 `login.html`、`dashboard.html`、`servers.html`、`tunnels.html`、`config.html`、`audit.html` 等独立模块页。
- [x] 客户端隧道页支持新增、编辑、删除本地绑定。
- [x] 服务端连接新增/编辑弹窗。

## 9. MCP AI 扩展接口

### 9.1 服务端 MCP

- [x] 增加 MCP 启停配置。
- [x] 增加 MCP 访问令牌或复用管理鉴权。
- [x] MCP 路由挂到同一个 HTTP 服务，使用 `/mcp/health` 和 `/mcp/tools/call`。
- [x] 实现 `server.list_clients`。
- [x] 实现 `server.get_client`。
- [x] 实现 `server.list_tunnels`。
- [x] 实现 `server.create_tunnel`。
- [x] 实现 `server.start_tunnel`。
- [x] 实现 `server.stop_tunnel`。
- [x] 实现 `server.delete_tunnel`。
- [x] 实现 `server.get_dashboard`。
- [x] MCP 修改类操作写入审计日志。

### 9.2 客户端 MCP

- [x] 增加 MCP 启停配置。
- [x] 增加 MCP 访问令牌或复用管理鉴权。
- [x] MCP 路由挂到同一个 HTTP 服务，使用 `/mcp/health` 和 `/mcp/tools/call`。
- [x] 实现 `client.list_servers`。
- [x] 实现 `client.connect_server`。
- [x] 实现 `client.disconnect_server`。
- [x] 实现 `client.list_tunnels`。
- [x] 实现 `client.get_network_status`。
- [x] MCP 修改类操作写入审计日志。

## 10. 异常处理与运维能力

- [x] 统一错误码。
- [x] 统一 panic recover 中间件。
- [x] 所有 TCP 连接关闭路径需要释放资源。
- [x] 端口冲突时返回明确错误。
- [x] 客户端离线时返回明确错误。
- [x] 本地服务不可达时返回明确错误。
- [x] 关键日志包含 request_id、client_id、tunnel_id、connection_id。
- [x] 支持 `error`、`info`、`debug` 日志级别。
- [x] 普通日志输出包含触发 Go 文件名和行号。
- [x] 支持日志按日期切分。
- [x] 审计日志写入 `logs/audit/YYYY-MM-DD.jsonl`。
- [x] 首次启动将旧 SQLite `audit_logs` 迁移到 JSONL，并写入迁移标记避免重复导出。
- [x] 支持 Windows、Linux、macOS 编译。

## 11. 测试与验收

### 11.1 单元测试

- [x] 测试配置加载。
- [x] 测试数据库初始化。
- [x] 测试密码哈希和校验。
- [x] 测试 JWT 签发、校验、过期。
- [x] 测试控制协议帧读写。
- [x] 测试客户端秘钥生成、哈希、校验。
- [x] 测试端口范围校验。

### 11.2 集成测试

- [x] 启动服务端后可访问 Web 管理端口。
- [x] 启动客户端后可访问本地 Web 管理端口。
- [x] 客户端使用授权秘钥成功连接服务端。
- [x] 服务端后台显示客户端在线。
- [x] 创建 TCP 隧道后公网端口可访问客户端本地 TCP 服务。
- [x] 服务端隧道不配置本地目标，客户端本地绑定配置决定 `local_host/local_port`。
- [x] 客户端缺少本地绑定或绑定禁用时，服务端展示可见错误。
- [x] 多客户端、多隧道并发运行。
- [x] 客户端断线后服务端显示离线。
- [x] 客户端恢复后自启动隧道自动恢复。
- [x] 停止隧道后公网端口释放。
- [x] 删除隧道后公网端口释放。
- [x] 禁用客户端后无法继续连接。
- [x] 服务端和客户端重启后配置、隧道、审计日志仍存在。
- [x] JWT 过期、刷新、鉴权、限流均按预期生效。
- [x] MCP 查询和控制接口返回预期结果。
- [x] MCP 通过 HTTP 端口下的 `/mcp/tools/call` 调用，不再启动独立 MCP 监听端口。
- [x] 开启 TLS 后客户端可正常连接服务端。
- [x] 关闭 TLS 后开发环境可正常连接服务端。

## 12. 建议开发顺序

1. 完成服务端和客户端基础工程。
2. 完成 SQLite 表结构、配置、普通日志、JSONL 审计日志、统一响应。
3. 完成 Web 登录、JWT、后台鉴权。
4. 完成服务端客户端授权管理 API。
5. 完成客户端服务端连接管理 API。
6. 完成控制连接认证、心跳、在线状态。
7. 完成隧道 CRUD 和服务端端口监听。
8. 完成数据连接绑定和 TCP 双向转发。
9. 完成流量统计、仪表盘、审计日志。
10. 完成服务端和客户端 Layui 前端页面。
11. 完成 MCP 接口。
12. 完成 TLS、限流、异常处理和跨平台构建。
13. 按验收清单完成测试和修复。
