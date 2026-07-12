# 小良内网穿透 (工具人小良-内网穿透软件)



这是一套AI智能体(通过MCP)都能使用的 TCP 内网穿透工具，它基于 Go语言 采用 C/S 架构，由公网服务端 `nattserver` 和内网客户端 `nattuser` 组成。服务端负责公网监听、客户端授权、隧道状态和流量统计；客户端负责连接服务端、绑定本地目标服务，并把公网访问流量转发到内网 TCP 服务。

当前版本只支持 TCP 端口穿透，不包含 UDP、HTTP 域名转发、P2P 打洞。

---

**注意: 使用或编译本软件前须阅读、理解并同意[免责声明](DISCLAIMER.md)的内容，若不同意可选择其他如:FRP、花生壳、Ngrok‌等同类软件。禁止利用本软件从事任何触犯中华人民共和国及其本软件部署地相关法律法规的行为。**

---

## 软件下载方式

1. 当前开源项目的发行版页 
2. 百度网盘链接：[https://pan.baidu.com/s/1SvAqFQfEbizim0t7q6beeQ?pwd=6666](https://pan.baidu.com/s/1SvAqFQfEbizim0t7q6beeQ?pwd=6666)

## 视频介绍和详细教程
- 详细教程：[https://www.bilibili.com/video/BV14FNg6QELL/?spm_id_from=333.337.search-card.all.click](https://www.bilibili.com/video/BV14FNg6QELL/?spm_id_from=333.337.search-card.all.click)

<iframe src="//player.bilibili.com/player.html?isOutside=true&aid=116906073922358&bvid=BV14FNg6QELL&cid=39878266355&p=1" scrolling="no" border="0" frameborder="no" framespacing="0" allowfullscreen="true"></iframe>
## 核心特性

- TCP 端口穿透，支持多客户端、多隧道并发。
- 服务端 Web 控制台：客户端授权、隧道管理、流量监控、配置、审计、MCP 管理。
- 客户端 Web 控制台：隧道连接、本地目标绑定、连接状态、配置、审计、MCP 管理。
- SQLite 持久化用户、客户端授权、隧道、连接、配置和流量统计。
- 首次启动初始化向导，支持自定义管理员账号密码、生产/测试模式、Web 控制台 HTTPS。
- 登录安全：SM2 加密密码、图片验证码、JWT、用户协议勾选、同 IP 登录失败递进封禁。
- 密码和秘钥哈希使用 SM3 盐值格式。
- 审计日志写入 `logs/audit/YYYY-MM-DD.jsonl`。
- MCP 使用标准 Streamable HTTP JSON-RPC，支持 Codex 等 AI 工具自动发现和调用。

## 项目结构

```text
当前项目目录路径\natt
  nattserver\    服务端项目
  nattuser\      客户端项目
  skills\        项目 AI Skill
```

服务端默认运行时母目录：

```text
nattserver\xiaoliang02_server\
  config\config.json
  data\nattserver.db
  data\sm2_private.pem
  data\sm2_public.pem
  logs\YYYY-MM-DD.log
  logs\audit\YYYY-MM-DD.jsonl
  ssl\web.crt
  ssl\web.key
```

客户端默认运行时母目录：

```text
nattuser\xiaoliang02_user\
  config\config.json
  data\nattuser.db
  data\sm2_private.pem
  data\sm2_public.pem
  logs\YYYY-MM-DD.log
  logs\audit\YYYY-MM-DD.jsonl
  ssl\web.crt
  ssl\web.key
```

默认启动只读取上述母目录中的 `config.json`。显式传入 `-config <path>` 时仍支持 `.json`、`.yaml`、`.yml`。

## 默认端口

服务端：

| 用途 | 默认值 |
| --- | --- |
| Web 管理后台 | `0.0.0.0:25510` |
| MCP 接口 | `0.0.0.0:25510/mcp` |
| 客户端控制连接 | `0.0.0.0:25511` |
| 客户端数据连接 | `0.0.0.0:25512` |
| 公网映射端口范围 | `0-65535` |

客户端：

| 用途 | 默认值 |
| --- | --- |
| Web 管理后台 | `127.0.0.1:25520` |
| MCP 接口 | `127.0.0.1:25520/mcp` |
| 默认服务端控制端口 | `25511` |
| 默认服务端数据端口 | `25512` |

启动前会检查本机监听端口是否被占用。服务端检查 Web、控制、数据端口；客户端检查本机 Web 端口。

## 快速开始

### 1. 启动服务端

```powershell
cd 当前项目目录路径\natt\nattserver
go run .
```

首次启动访问：

```text
http://127.0.0.1:25510/init.html
```

初始化页分两步：

1. 配置运行模式、端口、数据库路径和 Web 控制台 HTTPS。
2. 创建控制台管理员账号密码，并勾选“已阅读并同意《用户协议》”。

### 2. 启动客户端

```powershell
cd 当前项目目录路径\natt\nattuser
go run .
```

首次启动访问：

```text
http://127.0.0.1:25520/init.html
```

客户端初始化时只配置默认控制端口和默认数据端口，不配置默认服务端地址。服务端地址在新增隧道连接时填写。

### 3. 显式配置启动

```powershell
cd 当前项目目录路径\natt\nattserver
go run . -config xiaoliang02_server\config\config.json

cd 当前项目目录路径\natt\nattuser
go run . -config xiaoliang02_user\config\config.json
```

### 4. 编译

```powershell
cd 当前项目目录路径\natt\nattserver
go build -p 1 ./...

cd 当前项目目录路径\natt\nattuser
go build -p 1 ./...
```

编译后的 exe 放到空文件夹直接启动时，程序会以 exe 所在目录作为工作目录，并自动创建 `xiaoliang02_server/` 或 `xiaoliang02_user/`。

## 基本使用流程

1. 启动并初始化 `nattserver`。
2. 在服务端后台新增客户端授权，复制生成的客户端秘钥。
3. 在服务端后台新增 TCP 隧道，填写公网监听地址和公网监听端口；本地地址和本地端口不在服务端填写。
4. 根据需要勾选“nattuser 连接后自动启动”。
5. 启动并初始化 `nattuser`。
6. 在客户端后台新增隧道连接，填写服务端地址、控制端口、数据端口、服务端隧道秘钥、本地目标地址和本地目标端口。
7. 启动客户端隧道连接后，通过服务端公网端口访问内网 TCP 服务。

隧道归属规则：

- `nattserver` 管理公网监听端口、状态、秘钥和流量。
- `nattuser` 管理服务端地址、服务端端口、隧道秘钥和本地目标。
- 同一个服务端隧道同一时间只能被一个在线客户端占用。
- 服务端隧道在客户端未在线时点击“启动”，会进入“待连接”状态并开启自动启动；客户端上线后自动变为运行中。
- 客户端因网络异常断线后会自动重连；如果在客户端后台点击“断开”，会同时关闭该连接自启动，必须再次点击“连接”才恢复自动重连。
- 服务端状态详情为“隧道控制连接已离线”的可恢复错误，会在原客户端重新上线或心跳到达后自动恢复隧道。
- 服务端停止隧道后，会通过停止指令和心跳响应同步给客户端，客户端状态详情会显示“服务端暂停了隧道连接，请通知服务端人员启动隧道。”。

## MCP 使用

两端 MCP 都使用标准 Streamable HTTP JSON-RPC：

```text
POST http://<host>:<port>/mcp
```

鉴权方式：

```text
Authorization: Bearer <MCP_KEY>
```

或：

```text
X-MCP-Token: <MCP_KEY>
```

支持方法：

- `initialize`
- `notifications/initialized`
- `ping`
- `tools/list`
- `tools/call`

服务端常用工具：

- `server.list_clients`
- `server.get_client`
- `server.list_tunnels`
- `server.create_tunnel`（省略 `auto_start` 时默认 `true`，创建后进入待连接状态）
- `server.start_tunnel`
- `server.stop_tunnel`
- `server.delete_tunnel`
- `server.get_dashboard`

客户端常用工具：

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

客户端新增隧道连接工具参数：`name`、`server_host`、`client_secret`、`local_host`、`local_port` 为必填；`server_port`、`data_port` 可省略，省略时使用客户端配置里的默认控制端口和数据端口。

工具发现示例：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/list",
  "params": {}
}
```

工具调用示例：

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "method": "tools/call",
  "params": {
    "name": "server.list_tunnels",
    "arguments": {
      "page": 1,
      "page_size": 20
    }
  }
}
```

## 测试

服务端：

```powershell
cd 当前项目目录路径\natt\nattserver
go test ./... -count=1 -p 1
go build -p 1 ./...
```

客户端：

```powershell
cd 当前项目目录路径\natt\nattuser
go test ./... -count=1 -p 1
go build -p 1 ./...
```

如果 Windows 下测试临时 exe 被占用，可以设置独立 `GOTMPDIR` 后重试。

## 相关文档

- [需求文档.txt](需求文档.txt)
- [使用文档.md](使用文档.md)
- [开发任务拆分TODO.md](开发任务拆分TODO.md)
- [项目 AI Skill](skills/xiaoliang02-natt/SKILL.md)
- [项目模块地图](skills/xiaoliang02-natt/references/project-map.md)

## 代码仓库

GitCode：

```text
https://gitcode.com/gongjuliang/xiaoliang02
```
## 开源协议适用

### 1. 默认许可协议
本软件默认基于 **GNU Affero General Public License v3.0 (AGPL-3.0)** 开源协议发布。除非您符合下面的第 2 条规定的例外条件,否则您的使用、修改、分发行为均受 AGPL-3.0 约束。

AGPL-3.0 协议全文见项目根目录下的 `LICENSE` 文件,或访问:https://www.gnu.org/licenses/agpl-3.0.html

### 2. 例外许可条件
在同时满足以下全部条件时,您可获得本软件的 **MIT 开源协议** 授权:

1. **关注要求**:您在以下任一平台**关注**官方账号"**工具人小良**"成为粉丝,且关注状态持续有效:
    - 哔哩哔哩
    - 抖音
    - 快手
    - 小红书

2. **身份要求**:您的账号主体为**自然人**(非企业、非个体工商户)。

注:企业或个体工商户需要联系开发者本人获取MIT开源授权书，否则请遵循AGPL-3.0开源协议。

### 3. 授权限制与撤销
1. **不可转让**:MIT 授权仅关注账号本人使用,不得转让、出租、出借给第三方。

2. **持续义务**:获得 MIT 授权后,如您取消关注或将账号主体变更为企业/组织,授权自动终止。

3. **违规后果**:未完成例外许可条件且违反AGPL-3.0 开源协议的行为,开发者有权追究违约责任。已分发的衍生作品仍需遵守 AGPL-3.0 协议。

4. **追溯效力**:若 MIT 授权撤销/终止后，后续分发行为须恢复适用 AGPL-3.0。

### 4. 协议冲突处理
[免责声明](DISCLAIMER.md) 及 AGPL-3.0 协议共同构成完整的许可条款。冲突时优先级如下:
1. [免责声明](DISCLAIMER.md)中的强制性条款(特别是免责、责任限制条款)
2. AGPL-3.0 协议文本 (符合上述例外许可条件的自然人本项则为 MIT 协议)
