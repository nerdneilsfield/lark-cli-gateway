# lark-cli-gateway

一个只监听本机的 HTTP 网关：接收 JSON 通知，经有界 FIFO 队列限速转发给
`lark-cli`（通过 `lark-cli im +messages-send` 发送），
带固定间隔排队、失败重试与队列满则快速拒绝。发送方只需 `POST /send`，无需关心飞书
认证、命令参数与限流细节。

```mermaid
flowchart LR
    A[curl / python / lark-gateway-cli] -->|POST /send| B[lark-gateway-server]
    B -->|FIFO queue + retry + interval| C[lark-cli im +messages-send]
    C --> D[飞书/Lark]
```

- 两个二进制：`lark-gateway-server`（网关服务）与 `lark-gateway-cli`（命令行客户端）。
- 默认只监听 `127.0.0.1`，不在公网暴露任何端口。
- 发送经 `exec.Command` 直接调用 `lark-cli`，不经过 shell，不改写消息内容。

## 前置要求

- 构建需要 Go 1.23+；也可以直接从 GitHub Releases 下载对应平台的二进制
  （Linux / macOS / Windows × amd64 / arm64）。
- 运行 **server** 的机器必须已安装并登录 `lark-cli`（即 `lark-cli im` 可直接发消息）。
- 运行 **client** 的机器只需能访问 server 的地址。

## 安装

源码构建（产生 `lark-gateway-server` / `lark-gateway-cli` 两个二进制到仓库根目录）：

```bash
make build
```

从 Release 下载：解压后 `lark-gateway-server_{{ version }}_{{ platform }}/` 目录下即二进制。

## Server 部署

### 本地运行

```bash
make build
./lark-gateway-server
```

启动后监听 `127.0.0.1:19090`，验证存活：

```bash
curl -s -X POST http://127.0.0.1:19090/send \
  -H 'Content-Type: application/json' \
  -d '{"chat_id":"oc_xxx","as":"bot","type":"text","content":"hello"}'
```

### 配置参数

| Flag | 默认值 | 说明 |
|---|---|---|
| `-listen` | `127.0.0.1:19090` | HTTP 监听地址 |
| `-queue-size` | `100` | 有界队列容量，满则返回 503 |
| `-interval` | `1s` | 两条消息之间的固定间隔 |
| `-retries` | `2` | 单条消息的额外重试次数（2 => 最多发送 3 次） |
| `-retry-interval` | `2s` | 每次重试前的等待 |
| `-lark-cli` | `lark-cli` | `lark-cli` 可执行文件路径 |

### 常驻（systemd）

```ini
# /etc/systemd/system/lark-gateway-server.service
[Unit]
Description=lark-cli-gateway server
After=network.target

[Service]
ExecStart=/usr/local/bin/lark-gateway-server
Restart=on-failure
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now lark-gateway-server
```

macOS 可用 launchd 或直接 `nohup`；只要保证进程常驻即可。

### 服务器通过 autossh 反代连接本地 server

典型场景：`lark-cli` 的登录态在本地桌面（或某台内网机器）上，而云服务器上要发通知。
服务器无法直连桌面端口，用 SSH 反向隧道把**服务器自身的 `127.0.0.1:19090`** 转发回
桌面的网关：

```mermaid
flowchart LR
    A[云服务器上的发送方] -->|POST 127.0.0.1:19090| B[autossh 反代<br>127.0.0.1:19090 -> 桌面:19090]
    B -.SSH 加密.-> C[桌面 lark-gateway-server]
```

1. 在桌面启动网关：

   ```bash
   ./lark-gateway-server
   ```

2. 在服务器上建立反向隧道（需先配置好公钥登录桌面）：

   ```bash
   autossh -M 0 -N -f -R 19090:127.0.0.1:19090 \
     -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
     -o ExitOnForwardFailure=yes \
     <desktop-user>@<desktop-host>
   ```

   - `-R 19090:127.0.0.1:19090`：把服务器上的 `127.0.0.1:19090` 转发到桌面 `127.0.0.1:19090`。
   - `ExitOnForwardFailure=yes`：远端端口被占用或目标不通时立即失败退出，避免假存活。
   - `autossh` 负责断线重连；服务器只需有到桌面的 SSH 通道（通常出站 22），无需开放任何客户端端口。
   - 服务器上 `ss -lntp | grep 19090` 应看到 `127.0.0.1:19090`。

3. 服务器上直接使用 `http://127.0.0.1:19090/send` 即可（curl / python / cli 均可），
   流量经 SSH 加密回桌面由网关投递。

常驻隧道可放入 systemd：

```ini
# /etc/systemd/system/gateway-tunnel.service
[Unit]
Description=autossh reverse tunnel to lark-gateway-server
After=network-online.target

[Service]
ExecStart=autossh -M 0 -N -R 19090:127.0.0.1:19090 -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes -o StrictHostKeyChecking=no <desktop-user>@<desktop-host>
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

### 安全注意

- 网关**没有认证**。默认绑 `127.0.0.1` 即安全边界所在；如需对外，务必用 SSH 隧道
  或反代控制访问，不要改成 `0.0.0.0` 裸奔。
- 使用 `-R` 时，远端绑定地址受服务器 `GatewayPorts` 策略影响；确认只监听 `127.0.0.1`。

## Client 用法

协议就是单个 HTTP JSON 接口，三种方式互通。server 返回 `200 {"ok":true}` 表示
**已入队**；底层发送失败会在重试 `-retries` 次后丢弃并写 server 日志，不代表队列返回
时已成功投递。

### lark-gateway-cli

```bash
lark-gateway-cli send-msg --chat-id oc_xxx --text "hello"
lark-gateway-cli send-msg --chat-id oc_xxx --as user --markdown '**bold** and `code`'
```

| Flag | 默认值 | 说明 |
|---|---|---|
| `--host` | `$LARK_GATEWAY_HOST` 或 `127.0.0.1` | 网关主机（不含 scheme） |
| `--port` | `$LARK_GATEWAY_PORT` 或 `19090` | 网关端口 |
| `--chat-id` | `$LARK_CHAT_ID` | 必填，飞书会话 ID |
| `--as` | `bot` | `user` 或 `bot` |
| `--text` / `--markdown` | — | 二选一，消息内容 |

成功打印 `{"ok":true}`；失败打印错误到 stderr 并以非零码退出。客户端 HTTP 超时 5 秒。

### curl

```bash
curl -s -X POST http://127.0.0.1:19090/send \
  -H 'Content-Type: application/json' \
  -d '{"chat_id":"oc_xxx","as":"bot","type":"text","content":"hello from curl"}'
```

### Python 标准库

```python
import json
import urllib.error
import urllib.request

payload = json.dumps(
    {"chat_id": "oc_xxx", "as": "bot", "type": "markdown", "content": "**hi**"}
).encode()

req = urllib.request.Request(
    "http://127.0.0.1:19090/send", data=payload,
    headers={"Content-Type": "application/json"},
)
try:
    with urllib.request.urlopen(req) as resp:
        print(resp.read().decode())
except urllib.error.HTTPError as e:
    print(e.code, e.read().decode())
```

### HTTP API 约定

`POST /send`，`Content-Type: application/json`，body 为一个 JSON 对象：

| 字段 | 必填 | 取值 | 说明 |
|---|---|---|---|
| `chat_id` | 是 | string | 飞书会话 ID |
| `as` | 是 | `user` \| `bot` | 以哪个身份发送 |
| `type` | 是 | `text` \| `markdown` | 消息类型 |
| `content` | 是 | string | 正文 |

状态码：

| 状态码 | 含义 |
|---|---|
| `200` | `{"ok":true}`，已入队 |
| `400` | body 非法：非 JSON / 缺字段 / 值非法 / 不止一个 JSON 值 |
| `503` | 队列满，稍后重试 |

## 开发

```bash
make build        # 构建两个二进制
make check        # fmt-check + lint + vet + test + goreleaser check
make release-snapshot  # 本地生成全部发行产物到 dist/（不发布）
```

推送 `vMAJOR.MINOR.PATCH`（或 `vMAJOR.MINOR.PATCH-rc.N` 预发布）tag 会自动触发
GitHub Release：产出 12 个平台压缩包 + `checksums.txt` + 每包一份 SPDX SBOM +
SLSA provenance 证明。
