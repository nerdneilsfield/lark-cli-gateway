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

### 服务器经 SSH 隧道连接本地 server

典型场景：`lark-cli` 的登录态在本地桌面（或某台内网机器）上，而云服务器上要发通知。
目标一致：让**服务器上的 `127.0.0.1:19090`** 经 SSH 隧道转发到**桌面的网关**。
SSH 的 `-R`/`-L` 双向都能暴露端口，按**谁到谁可达**选一种：

| 方案 | 发起方 | 形式 | 适用 |
|---|---|---|---|
| A | 桌面 → 服务器 | `-R` | 桌面在 NAT 后、服务器有公网 IP（最常见） |
| B | 服务器 → 桌面 | `-L` | 桌面可被服务器访问（公网 IP / 端口转发 / 同一内网） |

记忆法：`-R` 的监听端口绑在 **SSH 服务端**，转发目标在**客户端侧**解析；`-L` 反之
（绑定在客户端，目标在服务端侧解析）。两种方案最终落点相同：服务器
`127.0.0.1:19090` → 桌面 `127.0.0.1:19090`（网关）。

#### 方案 A：桌面发起 `-R`（推荐，NAT 场景）

1. 桌面启动网关：

   ```bash
   ./lark-gateway-server
   ```

2. 桌面的 SSH 公钥加入服务器 `~/.ssh/authorized_keys`。
3. 桌面运行 autossh：

   ```bash
   autossh -M 0 -N -f -R 19090:127.0.0.1:19090 \
     -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
     -o ExitOnForwardFailure=yes \
     <server-user>@<server-host>
   ```

   - 端口 `19090` 绑定在**服务器**（sshd），目标 `127.0.0.1:19090` 在**桌面**侧解析，即网关。
   - `ExitOnForwardFailure=yes`：服务器侧 19090 被占用时立即失败退出，避免假存活。
   - autossh 连接超时自动重连；桌面只需出站访问服务器 22，服务器无需开放任何入站端口。
   - 验证：服务器上 `ss -lntp | grep 19090` 应看到 `127.0.0.1:19090`。

4. 服务器上直接使用 `http://127.0.0.1:19090/send`（curl / python / cli 均可），流量经
   SSH 加密回桌面由网关投递。

#### 方案 B：服务器发起 `-L`（桌面可达时）

1. 桌面启动网关（同方案 A 第 1 步）。
2. 服务器本机的 SSH 公钥加入桌面 `~/.ssh/authorized_keys`。
3. 服务器运行 autossh：

   ```bash
   autossh -M 0 -N -f -L 19090:127.0.0.1:19090 \
     -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
     -o ExitOnForwardFailure=yes \
     <desktop-user>@<desktop-host>
   ```

   - 端口 `19090` 绑定在**服务器本机**（ssh 客户端侧），目标 `127.0.0.1:19090` 在
     **桌面**侧解析，仍落在桌面的网关。
   - 前提：桌面的 sshd 可被服务器访问（22 公网 / 端口转发 / 同一内网）。
   - 验证同上：服务器上 `ss -lntp | grep 19090` 应看到 `127.0.0.1:19090`。

#### 常驻隧道

方案 A：autossh 放**桌面**（macOS 桌面用 launchd 或 tmux + nohup；Linux 桌面用 systemd）：

```ini
# /etc/systemd/system/gateway-tunnel.service  (跑在桌面)
[Unit]
Description=autossh reverse tunnel to server (gateway port 19090)
After=network-online.target

[Service]
ExecStart=autossh -M 0 -N -R 19090:127.0.0.1:19090 -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes <server-user>@<server-host>
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

方案 B：autossh 放**服务器**，unit 同上，仅把 `-R` 换成 `-L`、`<server-user>@<server-host>` 换成 `<desktop-user>@<desktop-host>`。

### 安全注意

- 网关**没有认证**。默认绑 `127.0.0.1` 即安全边界所在；如需对外，务必用 SSH 隧道
  或反代控制访问，不要改成 `0.0.0.0` 裸奔。
- 绑定位置随方案不同：`-R` 的远端绑定受服务器 sshd `GatewayPorts` 策略影响，`-L` 的
  绑定固定在客户端本机；两种都应确认只监听 `127.0.0.1`。

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
