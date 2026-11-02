#Requires -Version 5.1
param(
  [int]$Port = 8765,
  [string]$BindHost = '127.0.0.1'
)

$ErrorActionPreference = 'Stop'
$Root = $PSScriptRoot
$Releases = Join-Path $Root 'zcode\electron\releases'

if (-not (Test-Path -LiteralPath $Releases -PathType Container)) {
  throw "缺少 releases 目录：$Releases"
}

function Get-OfflineVersions {
  Get-ChildItem -LiteralPath $Releases -Directory -ErrorAction SilentlyContinue |
    Where-Object { $_.Name -match '^\d+\.\d+\.\d+' } |
    ForEach-Object {
      $manifests = Get-ChildItem -LiteralPath $_.FullName -File -ErrorAction SilentlyContinue |
        Where-Object { $_.Name -like 'manifest-*.json' -or $_.Name -eq 'source-server-release.json' }
      if ($manifests) {
        [pscustomobject]@{
          Version   = $_.Name
          Manifests = @($manifests.Name)
        }
      }
    } |
    Sort-Object { [version]($_.Version -replace '[^\d.].*$','') } -Descending
}

$versions = @(Get-OfflineVersions)
if ($versions.Count -eq 0) {
  throw "在 $Releases 下未找到任何带 manifest 的版本目录（如 3.4.2）。"
}

$prefix = '/zcode/electron/releases'
$baseUrl = "http://${BindHost}:${Port}${prefix}"

Write-Host ''
Write-Host 'ZCode 离线 CDN 服务（多版本自动响应）'
Write-Host "  根目录   : $Root"
Write-Host "  监听地址 : http://${BindHost}:${Port}/"
Write-Host "  CDN 根   : $baseUrl"
Write-Host "  已发现版本: $($versions.Version -join ', ')"
Write-Host ''
Write-Host '请设置以下用户环境变量，然后完全退出并重启 ZCode：'
Write-Host "  ZCODE_REMOTE_ASSET_CDN_BASE_URL=$baseUrl"
Write-Host ''
Write-Host '自检地址：'
foreach ($v in $versions) {
  foreach ($m in $v.Manifests) {
    Write-Host "  $baseUrl/$($v.Version)/$m"
  }
}
Write-Host ''
Write-Host '提示：推荐从 Release 下载 zcode-cdn-mock 二进制，或使用 ./docker-up.sh'
Write-Host '按 Ctrl+C 停止服务。'
Write-Host ''

$listener = [System.Net.HttpListener]::new()
$listener.Prefixes.Add("http://${BindHost}:${Port}/")
try {
  $listener.Start()
} catch {
  throw "无法绑定 http://${BindHost}:${Port}/。请换一个 -Port，或必要时以管理员身份运行。$_"
}

function Get-ContentType([string]$Path) {
  switch -Regex ($Path) {
    '\.json$' { return 'application/json; charset=utf-8' }
    '\.tar\.gz$' { return 'application/gzip' }
    '\.txt$' { return 'text/plain; charset=utf-8' }
    '\.md$' { return 'text/markdown; charset=utf-8' }
    default { return 'application/octet-stream' }
  }
}

function Send-JsonResponse($Response, [int]$Status, $Object) {
  $json = ($Object | ConvertTo-Json -Depth 6)
  $buf = [Text.Encoding]::UTF8.GetBytes($json)
  $Response.StatusCode = $Status
  $Response.ContentType = 'application/json; charset=utf-8'
  $Response.ContentLength64 = $buf.LongLength
  $Response.OutputStream.Write($buf, 0, $buf.Length)
}

try {
  while ($listener.IsListening) {
    $ctx = $listener.GetContext()
    $req = $ctx.Request
    $res = $ctx.Response
    try {
      $absPath = $req.Url.AbsolutePath

      if ($absPath -eq '/versions.json' -or $absPath -eq '/versions') {
        $fresh = @(Get-OfflineVersions)
        Send-JsonResponse $res 200 @{
          platformHint = 'linux-x64'
          cdnBasePath  = $prefix
          count        = $fresh.Count
          versions     = @(
            $fresh | ForEach-Object {
              @{ version = $_.Version; manifests = @($_.Manifests) }
            }
          )
        }
        Write-Host "[200] $($req.HttpMethod) $absPath ($($fresh.Count) versions)"
        continue
      }

      if ($absPath -eq '/healthz' -or $absPath -eq '/health') {
        Send-JsonResponse $res 200 @{ ok = $true; root = $Root }
        Write-Host "[200] $($req.HttpMethod) $absPath"
        continue
      }

      $rel = [Uri]::UnescapeDataString($absPath.TrimStart('/')) -replace '/', [IO.Path]::DirectorySeparatorChar
      $full = [IO.Path]::GetFullPath((Join-Path $Root $rel))
      $rootFull = [IO.Path]::GetFullPath($Root)

      if (-not $full.StartsWith($rootFull, [StringComparison]::OrdinalIgnoreCase)) {
        $res.StatusCode = 403
        $buf = [Text.Encoding]::UTF8.GetBytes('禁止访问')
        $res.OutputStream.Write($buf, 0, $buf.Length)
      } elseif (-not (Test-Path -LiteralPath $full -PathType Leaf)) {
        $res.StatusCode = 404
        $msg = "未找到：$absPath"
        Write-Host "[404] $msg"
        $buf = [Text.Encoding]::UTF8.GetBytes($msg)
        $res.OutputStream.Write($buf, 0, $buf.Length)
      } else {
        $bytes = [IO.File]::ReadAllBytes($full)
        $res.StatusCode = 200
        $res.ContentType = Get-ContentType $full
        $res.ContentLength64 = $bytes.LongLength
        $res.OutputStream.Write($bytes, 0, $bytes.Length)
        Write-Host "[200] $($req.HttpMethod) $absPath ($([math]::Round($bytes.Length/1MB, 2)) MB)"
      }
    } catch {
      Write-Host "[错误] $_"
      try { $res.StatusCode = 500 } catch {}
    } finally {
      try { $res.OutputStream.Close() } catch {}
    }
  }
} finally {
  $listener.Stop()
  $listener.Close()
}
