# ZCode 离线资源拉取（有网维护）

本目录用于在**可访问公网 CDN** 的机器上，拉取 / 更新远程开发资源，并写入旁边的 `../offline` 镜像目录。

> 仅处理 **linux-x64**。  
> 离线环境的使用方法见 [`../offline/README.md`](../offline/README.md)。

---

## 目录关系

```text
resources/
├── online/                         ← 本目录（有网维护）
│   ├── README.md
│   ├── go.mod
│   └── main.go / release.go        ← Go 下载工具
└── offline/                        ← 下载目标 / 离线分发包
    ├── README.md
    ├── main.go / go.mod              ← CDN 服务本体
    ├── Dockerfile / docker-up.sh     ← 可选 Docker 包装
    ├── serve.ps1
    ├── sha256sums-linux-x64.txt
    └── zcode/electron/releases/...
```

---

## 前置条件

- 能访问以下任一 CDN：
  - `https://cdn-zcode.z.ai/zcode/electron/releases`
  - `https://cdn.codegeex.cn/zcode/electron/releases`
  - `https://cdn.zcode-ai.com/zcode/electron/releases`
- 从 [GitHub Releases](https://github.com/coloraven/zcode_remote_offline/releases) 下载 `download-offline-assets_<os>_<arch>[.exe]`（日常使用无需 Go）

---

## 用法（推荐：Release 二进制）

将二进制放在 `online/` 目录下执行（写入同级 `../offline`）：

```powershell
# 列出 CDN 上可用版本（并标注本地是否已有）
.\download-offline-assets_windows_amd64.exe -list

.\download-offline-assets_windows_amd64.exe -version 3.16.3
.\download-offline-assets_windows_amd64.exe -version 3.4.2
.\download-offline-assets_windows_amd64.exe -version 3.5.0 -force
.\download-offline-assets_windows_amd64.exe -version 3.4.2 -cdn https://cdn-zcode.z.ai/zcode/electron/releases
.\download-offline-assets_windows_amd64.exe -help
```

```bash
chmod +x download-offline-assets_linux_amd64
./download-offline-assets_linux_amd64 -list
./download-offline-assets_linux_amd64 -version 3.16.3
```

不传 `-version` 时，会尝试使用 `offline` 里已有的最高版本。

> `-list` 会读取 update feed / changelog 收集候选，再探测实际资源清单（CDN 本身无目录索引，列表不保证包含所有历史版本）。Linux update feed 返回 404 时，会尝试其他桌面平台的 feed；所有 feed 都不可用时，仍会使用 changelog 和本地版本继续探测。
>
> 已知客户端版本时，可以直接下载，不需要先运行 `-list`；也可以用 `-list -version 3.16.3` 补充候选版本。若没有发现任何有效清单，命令会以非零状态退出，并显示具体请求地址和错误。

---

## 开发（可选）

改源码时需要 Go 1.22+：

```powershell
go run . -version 3.4.2
go build -trimpath -ldflags="-s -w" -o download-offline-assets.exe .
.\download-offline-assets.exe -version 3.4.2
```

---

## 工具会做什么

1. 优先读取 `<version>/source-server-release.json`（3.16.1 起的新格式）；不存在（404/410）时回退到 `manifest-linux-x64.json`
2. 新格式选择 `archives` 中 `target = linux-x64` 的完整 server 包，按清单 `urls` 下载，保存到 `<version>/<fileName>`；旧格式继续下载 `components` 下的组件包
3. 按清单校验 SHA256，新格式同时校验 `sizeBytes`；缓存校验通过时跳过下载，强制刷新失败时保留原缓存
4. 更新 `offline/sha256sums-linux-x64.txt`

清单仅在所需资源全部下载并校验成功后写入。官方新版使用相对 URL，清单原样保留；如果清单提供多个镜像或绝对 URL，下载器会依次尝试，并将所选 Linux x64 条目的 URL 改为本地文件名，供离线客户端使用。其他平台的包不会下载。

对应线上路径：

| 资源 | URL 形态 |
|------|----------|
| 新版清单（3.16.1+） | `{CDN}/<version>/source-server-release.json` |
| 新版完整包 | `{CDN}/<version>/zcode-server-linux-x64.tar.gz`（以清单 `urls` 为准） |
| 旧版清单 | `{CDN}/<version>/manifest-linux-x64.json` |
| 旧版组件包 | `{CDN}/components/linux-x64/<id>/<version>.tar.gz` |

校验清单中的路径以 `offline/zcode/electron/releases/` 为根：

```bash
cd ../offline/zcode/electron/releases
sha256sum -c ../../../sha256sums-linux-x64.txt
```

---

## 更新后如何分发到离线机

1. 在本机（有网）跑完下载工具
2. 将整个 `offline/` 目录拷贝到离线电脑
3. 在离线机按 [`../offline/README.md`](../offline/README.md) 用 Release 二进制或 Docker 启动本地 CDN，并配置环境变量

多版本可反复执行并指定不同 `-version`；离线服务会按请求路径自动响应，无需改配置。

不必把 `online/` 拷到离线环境（离线机不需要下载工具）。

---

## 常见问题

### 报错「没有可用的 CDN」

查看错误中的完整请求地址和 HTTP 状态。更新 feed 的 404 与资源清单的 404 含义不同；前者不代表整个 CDN 不可用。

已知客户端版本时，先直接指定版本，跳过 feed 发现：

```bash
./download-offline-assets_linux_amd64 -version 3.16.3
# 或指定可达的 releases 根地址（不带版本号/清单文件名）
./download-offline-assets_linux_amd64 -version 3.16.3 -cdn https://cdn-zcode.z.ai/zcode/electron/releases
```

版本号必须与已安装客户端一致。如果所有新旧清单地址均为 404，该镜像可能尚未发布或已移除这个版本，需换可用 CDN 或提供对应资源。

### 报错「appVersion 不匹配」

CDN 上该版本清单的 `appVersion` 与传入版本不一致，请确认版本号是否写对。

### 只想更新某个版本、保留旧版本文件

直接指定新版本即可；旧版本目录不会被自动删除。若不需要旧包可手动删 `offline/zcode/electron/releases/<旧版本>`。
