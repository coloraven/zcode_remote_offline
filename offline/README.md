# ZCode 安装版离线远程开发（仅 linux-x64）

本文说明如何在**无法访问公网 CDN** 的环境中，用本目录作为本地资源源，让已安装的 ZCode 客户端向 **Linux x64** SSH 远程主机部署并连接远程开发服务。

> 适用范围：ZCode **安装版**，远程服务器 **linux-x64**。  
> 支持目录内**多版本并存**：客户端请求哪个版本的 manifest，就自动响应哪个。  
> 不适用于开发态 `mockCdnDir`（安装包不会启用该路径）。  
> 有网环境下的资源拉取 / 版本更新见 [`../online/README.md`](../online/README.md)。

---

## 原理简述

安装版连接 SSH 远程时会：

**3.16.1 及以后版本**请求 `<version>/source-server-release.json`，选择目标平台的 archive，再下载同版本目录中的完整 server 包。离线目录需同时包含清单和 `zcode-server-linux-x64.tar.gz`。

**旧版本**使用以下组件流程：

1. 请求 CDN 上的 `manifest-linux-x64.json`
2. 按 manifest 下载组件包（`server-bundle` / `node-runtime` / `node-pty` / `glm` / `ripgrep`）
3. 解压到本机 `%APPDATA%\ZCode\remote-assets-cache`
4. 经 SFTP 上传到远程 `~/.zcode/server` 并启动

离线做法：把 CDN 目录镜像到本机，用本地 HTTP 服务MOCK CDN，再通过环境变量把客户端指过去。

```
本机 ZCode ──HTTP──► 本地 CDN（本目录 Go 服务，可选 Docker 包装）
                 │
                 └──SFTP──► Linux x64 远程 ~/.zcode/server
```

---

## 目录内容

```text
offline/
├── README.md
├── main.go / go.mod                   ← CDN 服务本体（独立 Go 程序）
├── Dockerfile / docker-compose*.yml / docker-up.sh  ← 可选：Docker（构建时编译）
├── serve.ps1                          ← 可选：无 Go 时的 Windows 本机服务
├── sha256sums-linux-x64.txt
└── zcode/electron/releases/
    ├── 3.4.2/
    │   └── manifest-linux-x64.json
    ├── 3.5.0/                         ← 可并存多个版本
    │   └── manifest-linux-x64.json
    ├── 3.16.3/
    │   ├── source-server-release.json
    │   └── zcode-server-linux-x64.tar.gz
    └── components/linux-x64/
        └── <id>/<version>.tar.gz
```

对应线上地址形态：

| 资源 | URL 形态 |
|------|----------|
| 新版清单 | `{CDN}/<version>/source-server-release.json` |
| 新版完整包 | `{CDN}/<version>/zcode-server-linux-x64.tar.gz` |
| 旧版清单 | `{CDN}/<version>/manifest-linux-x64.json` |
| 旧版组件包 | `{CDN}/components/linux-x64/<id>/<version>.tar.gz` |

其中 `{CDN}` 默认可理解为：`https://cdn-zcode.z.ai/zcode/electron/releases`

---

## 启动本地 CDN（推荐：Release 二进制）

从 [GitHub Releases](https://github.com/coloraven/zcode_remote_offline/releases) 下载对应平台的 `zcode-cdn-mock_<os>_<arch>[.exe]`，放到本目录执行：

```bash
chmod +x zcode-cdn-mock_linux_amd64
./zcode-cdn-mock_linux_amd64
```

```powershell
.\zcode-cdn-mock_windows_amd64.exe
```

默认：`http://127.0.0.1:8765`（可用环境变量 `PORT` / `BIND` / `CDN_ROOT` 覆盖）。

```bash
PORT=9000 BIND=0.0.0.0 ./zcode-cdn-mock_linux_amd64
```

### 自检

| 地址 | 说明 |
|------|------|
| `http://127.0.0.1:8765/` | 首页，列出已发现版本 |
| `http://127.0.0.1:8765/versions.json` | 机器可读版本列表（每次请求扫描目录） |
| `http://127.0.0.1:8765/healthz` | 健康检查 |
| `http://127.0.0.1:8765/zcode/electron/releases/3.16.3/source-server-release.json` | 新版清单 |
| `http://127.0.0.1:8765/zcode/electron/releases/3.4.2/manifest-linux-x64.json` | 某版本清单 |

### 多版本自动响应

- 用 [`../online`](../online) 再拉一个版本（例如 `3.5.0`）进本目录后，**无需改代码**；进程保持运行即可按路径响应。
- 客户端请求 `…/3.4.2/manifest-…` 或 `…/3.5.0/manifest-…` 时，有对应文件即返回。
- `ZCODE_REMOTE_ASSET_CDN_BASE_URL` 始终指向**不带版本号**的 releases 根即可。

---

## 可选：Docker 拉起（Linux amd64 / arm64）

与直接跑二进制二选一。同一套文件支持两种架构：

```bash
chmod +x docker-up.sh
./docker-up.sh              # 自动检测本机架构
./docker-up.sh --amd64      # 强制 linux/amd64
./docker-up.sh --arm64      # 强制 linux/arm64
PORT=9000 ./docker-up.sh
./docker-up.sh --down
```

或使用 compose 覆盖文件：

```bash
docker compose -f docker-compose.yml -f docker-compose.amd64.yml up -d --build
docker compose -f docker-compose.yml -f docker-compose.arm64.yml up -d --build
```

| 文件 | 说明 |
|------|------|
| `Dockerfile` | 多架构源码构建（构建时编译，BuildKit `TARGETARCH`） |
| `docker-compose.yml` | 源码构建启动 |
| `docker-compose.amd64.yml` / `docker-compose.arm64.yml` | 显式架构 |
| `docker-up.sh` | 一键启停 |

克隆本仓库后可自行构建；也可直接使用 Release 中的 `zcode-cdn-mock_docker_image_linux_*.tar.gz`（见根目录 README 方式 B）：

```bash
docker build --platform linux/amd64 -t zcode-cdn-mock:local .
# 或
./docker-up.sh --amd64
```

---

## 可选：PowerShell 本机服务（无 Go / 无 Docker）

```powershell
.\serve.ps1
.\serve.ps1 -Port 9000
```

默认只绑定 `127.0.0.1`。

---

## 配置环境变量

为当前用户设置（推荐）：

```powershell
[System.Environment]::SetEnvironmentVariable(
  'ZCODE_REMOTE_ASSET_CDN_BASE_URL',
  'http://127.0.0.1:8765/zcode/electron/releases',
  'User'
)
```

若 CDN 跑在局域网另一台机器：

```powershell
[System.Environment]::SetEnvironmentVariable(
  'ZCODE_REMOTE_ASSET_CDN_BASE_URL',
  'http://192.168.x.x:8765/zcode/electron/releases',
  'User'
)
```

可选：自定义本机资源缓存目录：

```powershell
[System.Environment]::SetEnvironmentVariable(
  'ZCODE_REMOTE_ASSET_CACHE_DIR',
  'D:\zcode-remote-assets-cache',
  'User'
)
```

**必须完全退出并重新打开 ZCode**，环境变量才会生效。

> 说明：不要设置 `ZCODE_DEV_REMOTE_ASSET_USE_CDN`（那是开发态开关）。  
> `ZCODE_REMOTE_ASSET_CDN_BASE_URL` 指向**不带版本号**的 releases 根目录即可；客户端会自动拼版本路径。

---

## 连接 Linux x64 远程

在 ZCode 中新建 / 打开 SSH Remote（目标机需为 linux-x64），正常连接即可。

成功时连接日志大致应出现：

```text
remoteCdnBaseUrl: http://127.0.0.1:8765/zcode/electron/releases
remote env: linux-x64
downloading http://127.0.0.1:8765/.../manifest-linux-x64.json
...
handshake done, server version: 3.4.2
```

不应再出现对 `cdn-zcode.z.ai` / `cdn.codegeex.cn` 的访问。

---

## 环境变量一览

| 变量 | 必填 | 含义 |
|------|------|------|
| `ZCODE_REMOTE_ASSET_CDN_BASE_URL` | 是 | 本地 CDN 的 releases 根，例如 `http://127.0.0.1:8765/zcode/electron/releases` |
| `ZCODE_REMOTE_ASSET_CACHE_DIR` | 否 | 本机解压缓存目录；默认 `%APPDATA%\ZCode\remote-assets-cache` |

---

## 校验文件完整性

```powershell
cd <本 offline 目录>
Get-FileHash -Algorithm SHA256 .\zcode\electron\releases\components\linux-x64\*\*\*.tar.gz |
  Format-Table Hash, Path -AutoSize
```

对照 `sha256sums-linux-x64.txt`。清单中的哈希来自旧版 `components[].sha256` 或新版 `archives[].sha256`。新版完整包放在版本目录下，不在 `components/` 下；Linux 可执行：

```bash
cd zcode/electron/releases
sha256sum -c ../../../sha256sums-linux-x64.txt
```

---

## 多机分发

将整个 `offline` 目录拷到离线电脑后：

1. 执行 Release 二进制 `zcode-cdn-mock_*`（或 Docker / `.\serve.ps1`）
2. 设置同样的 `ZCODE_REMOTE_ASSET_CDN_BASE_URL`
3. 重启 ZCode 再连远程

若希望跳过首次解压下载，也可额外拷贝已预热的：

`%APPDATA%\ZCode\remote-assets-cache`

但**仍需本地 CDN 可访问**（客户端会对 manifest 做在线校验/刷新，尤其是 `glm` 组件）。

---

## 常见问题

### 日志仍显示 `mockCdnDir: <missing>`

正常。安装版不会启用 `mockCdnDir`，离线依赖的是 `ZCODE_REMOTE_ASSET_CDN_BASE_URL`。

### 仍在访问公网 CDN

- 确认环境变量对「启动 ZCode 的进程」可见（设完后需重启客户端）
- 确认 CDN 服务（二进制 / Docker / `serve.ps1`）仍在运行
- 确认 URL 能打开 manifest（见上文自检链接）

### 远程不是 linux-x64

本目录**只准备了 linux-x64**。arm64 / 其他架构需要另下对应 manifest 与组件包。

### 客户端版本与目录里版本不一致

manifest 的 `appVersion` 必须与客户端一致。用 [`../online`](../online) 拉取对应版本后放入本目录即可；多版本可并存，服务会按请求路径自动响应。

### 端口被占用

```bash
PORT=9876 ./zcode-cdn-mock_linux_amd64
# 或
PORT=9876 ./docker-up.sh
```

```powershell
.\zcode-cdn-mock_windows_amd64.exe
# 设置环境变量 PORT=9876 后启动；或
.\serve.ps1 -Port 9876
```

同时把环境变量改成新端口。

### Docker 相关

- `未找到 docker`：改用 Release 二进制直接运行即可。
- 强制重建镜像：`docker compose build --no-cache && docker compose up -d`
- 查看日志：`docker logs -f zcode-cdn-mock`

### 开发（可选）

改本仓库源码时可用：

```bash
go run .
go build -trimpath -ldflags="-s -w" -o zcode-cdn-mock .
```

日常使用请下载 Release 二进制，不必安装 Go。

## 版本记录（当前已收录示例）

| 项目 | 值 |
|------|----|
| ZCode 客户端（示例） | 3.4.2 |
| 远程平台 | linux-x64 |
| server-bundle | v3.4.2+1d3aaaa777d7 |
| node-runtime | v22.16.0+7d37ff1b4544 |
| node-pty | v1.2.0-beta.10+524740883ddc |
| glm | v0.13.3+8fa56d5ce3ae |
| ripgrep | v13.0.0-10+418d9804e442 |

以 `versions.json` / 目录内实际版本为准。
