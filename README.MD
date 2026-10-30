# ZCode 远程开发包下载MOCK服务
## 项目用途
ZCode 连接 SSH 远程（linux-x64）时，需要在线从 CDN 拉取 `manifest` 与组件包（`server-bundle` / `node-runtime` 等），再部署到远程主机。

从 **3.16.1** 起，清单改为 `source-server-release.json`，资源改为完整的 `zcode-server-linux-x64.tar.gz`。本项目兼容新旧两种格式；例如在 `online/` 中执行 `go run . -version 3.16.3`，再在 `offline/` 中执行 `go run .`。已发布的旧版下载器需要重新构建或升级到包含此修复的版本。

本项目解决**离线环境** 的环境中，ZCode 无法连CDN拉取，ssh连接远程服务器进行开发的问题。

本仓库做两件事：
1. **有网侧**：从官方 CDN 把指定版本资源镜像到本地 `offline/` 目录  
2. **离线侧**：用本地 HTTP 服务（或 Docker）MOCK 线上 CDN 路径，让客户端通过环境变量 `ZCODE_REMOTE_ASSET_CDN_BASE_URL` 走内网/本机地址完成下载

支持多版本资源并存，按客户端请求的版本路径自动响应。日常使用下载 [Releases](https://github.com/coloraven/zcode_remote_offline/releases) 的二进制或 Docker 镜像包即可，无需安装 Go。

## 项目结构

| 目录 | 用途 |
|------|------|
| [`online/`](online/) | 有网环境：从公网 CDN 拉取 / 更新资源到 `offline/` |
| [`offline/`](offline/) | 离线环境：本地 HTTP 服务 MOCK 线上 CDN（多版本自动响应） |

正常使用请从 [GitHub Releases](https://github.com/coloraven/zcode_remote_offline/releases) 下载对应平台二进制，或 Linux 的 `zcode-cdn-mock_docker_image_*.tar.gz` 镜像包（约 2MB）。

---
## 使用方法
### 1. 有网拉取资源

下载 `download-offline-assets_<os>_<arch>[.exe]`，放到仓库旁（与 `offline/` 同级的 `online/` 工作方式：工具会写入 `../offline`）。在含 `offline` 目录的上一级结构中执行更直观：把二进制放进 `online/` 再运行。

```powershell
# Windows 示例（amd64）
.\download-offline-assets_windows_amd64.exe -list
.\download-offline-assets_windows_amd64.exe -version 3.4.2
.\download-offline-assets_windows_amd64.exe -version 3.5.0 -force
```

```bash
# Linux 示例
chmod +x download-offline-assets_linux_amd64
./download-offline-assets_linux_amd64 -list
./download-offline-assets_linux_amd64 -version 3.16.3
```

资源写入 `offline/zcode/electron/releases/...`。

---

### 2. 离线提供 CDN 服务

先准备好已拉取的 `offline/` 资源目录，再任选其一：

#### 方式 A：直接跑二进制

从 Release 下载 `zcode-cdn-mock_<os>_<arch>[.exe]`，放到 `offline/` 目录下执行：

```powershell
# Windows
cd offline
.\zcode-cdn-mock_windows_amd64.exe
```

```bash
# Linux
cd offline
chmod +x zcode-cdn-mock_linux_amd64
./zcode-cdn-mock_linux_amd64
```

默认：`http://127.0.0.1:8765`  
CDN 根：`http://127.0.0.1:8765/zcode/electron/releases`

```bash
PORT=9000 BIND=0.0.0.0 ./zcode-cdn-mock_linux_amd64
```

#### 方式 B：Docker（Linux amd64 / arm64）— 仅离线 CDN 服务（推荐）

Docker **只用于**离线侧的本地 CDN HTTP 服务。有网侧 `download-offline-assets_*` **不需要** Docker。

Release 已提供打好的镜像包（约 2MB）：`zcode-cdn-mock_docker_image_linux_amd64.tar.gz` / `…_arm64.tar.gz`。

##### B1. 下载 Release 镜像包

从 [Releases](https://github.com/coloraven/zcode_remote_offline/releases) 下载对应架构的：

- `zcode-cdn-mock_docker_image_linux_amd64.tar.gz`，或
- `zcode-cdn-mock_docker_image_linux_arm64.tar.gz`

与已拉取的 `offline/` 资源（至少含 `zcode/`）一起拷到离线机。

##### B2. 离线机加载镜像并启动

```bash
# 以 amd64 为例
docker load -i zcode-cdn-mock_docker_image_linux_amd64.tar.gz

# 数据目录：已拉取的 offline 资源（目录内需有 zcode/）
DATA_DIR=/path/to/offline

docker run -d --restart unless-stopped \
  --name zcode-cdn-mock \
  -p 8765:80 \
  -v "${DATA_DIR}:/data:ro" \
  -e CDN_ROOT=/data -e PORT=80 -e BIND=0.0.0.0 \
  zcode-cdn-mock:local
```

停止 / 删除容器：

```bash
docker stop zcode-cdn-mock
docker rm zcode-cdn-mock
```

默认：`http://127.0.0.1:8765`，CDN 根：`http://127.0.0.1:8765/zcode/electron/releases`。

##### （可选）自行构建镜像

开发或定制时可克隆仓库后本地构建（与 CI 相同，官方 `golang` 基础镜像）：

```bash
git clone https://github.com/coloraven/zcode_remote_offline.git
cd zcode_remote_offline/offline
docker build --platform linux/amd64 -t zcode-cdn-mock:local .
docker save zcode-cdn-mock:local | gzip > zcode-cdn-mock_docker_image_linux_amd64.tar.gz
```

---

### 3. 配置 ZCode 客户端环境变量

#### Windows（用户级，持久生效）

```powershell
[System.Environment]::SetEnvironmentVariable(
  'ZCODE_REMOTE_ASSET_CDN_BASE_URL',
  'http://127.0.0.1:8765/zcode/electron/releases',
  'User'
)
```

#### Linux / macOS（当前 shell 临时生效）

```bash
export ZCODE_REMOTE_ASSET_CDN_BASE_URL='http://127.0.0.1:8765/zcode/electron/releases'
```

#### Linux / macOS（持久化，bash）

```bash
echo "export ZCODE_REMOTE_ASSET_CDN_BASE_URL='http://127.0.0.1:8765/zcode/electron/releases'" >> ~/.bashrc
```

若 CDN 跑在另一台机器，把地址改成例如 `http://192.168.x.x:8765/zcode/electron/releases`。

然后**完全退出并重启 ZCode**，再进行 SSH 远程连接。

**使用注意：** 在 ZCode 远程连接相关设置中，`资源下载方式` 必须选 **`本地下载后上传`**（由本机经本地 CDN 下载资源，再上传到远程；不要选远程主机直接拉公网 CDN）。
---


## 开发（可选）

改代码时可用源码调试，日常使用不需要：

```powershell
cd online;  go run . -version 3.4.2
cd offline; go run .
```

详见 [`offline/README.md`](offline/README.md)、[`online/README.md`](online/README.md)。

## Release 产物一览

| 产物 | 说明 |
|------|------|
| `download-offline-assets_<os>_<arch>[.exe]` | 有网拉取工具 |
| `zcode-cdn-mock_<os>_<arch>[.exe]` | 离线 CDN 服务二进制 |
| `zcode-cdn-mock_docker_image_linux_amd64.tar.gz` | Linux amd64 Docker 镜像包（约 2MB） |
| `zcode-cdn-mock_docker_image_linux_arm64.tar.gz` | Linux arm64 Docker 镜像包（约 2MB） |
| `SHA256SUMS.txt` | 校验 |

平台：二进制为 linux / windows × amd64 / arm64；Docker 镜像包仅 linux amd64 / arm64。
