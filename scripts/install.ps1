# =============================================================================
# MossTerm 一键安装依赖（Windows / PowerShell 5.1+ / PowerShell 7+）
# -----------------------------------------------------------------------------
# 安装：
#   - Go（如未安装，使用 winget 或 choco）
#   - Node.js 20.x + npm
#   - Wails CLI v2（通过 go install）
#   - golangci-lint
#   - MSVC 构建工具提示（由 Wails doctor 验证）
#
# 用法（PowerShell）：
#   .\scripts\install.ps1                 # 默认：全部安装
#   .\scripts\install.ps1 -NoGo           # 跳过 Go
#   .\scripts\install.ps1 -NoWails        # 跳过 Wails CLI
#   .\scripts\install.ps1 -Help
#
# 注意：可能需要「以管理员身份运行」以安装全局工具。
# =============================================================================

[CmdletBinding()]
param(
    [switch]$NoGo,
    [switch]$NoNode,
    [switch]$NoWails,
    [switch]$NoLint,
    [switch]$Help
)

$ErrorActionPreference = "Stop"
$ProgressPreference    = "SilentlyContinue"

# —— 配置 ————————————————————————————————————————————————————————————
$GO_VERSION       = $env:GO_VERSION;       if (-not $GO_VERSION) { $GO_VERSION       = "1.22.0" }
$NODE_VERSION     = $env:NODE_VERSION;     if (-not $NODE_VERSION) { $NODE_VERSION     = "20" }
$WAILS_VERSION    = $env:WAILS_VERSION;    if (-not $WAILS_VERSION) { $WAILS_VERSION    = "v2.9.2" }
$GOLANGCI_VERSION = $env:GOLANGCI_VERSION; if (-not $GOLANGCI_VERSION) { $GOLANGCI_VERSION = "latest" }

# —— 颜色（Win10+ 支持 VT，Win7/Server 退化） ————————————————————————
function Write-Info  { param($m) Write-Host "[info]  $m" -ForegroundColor Cyan }
function Write-Ok    { param($m) Write-Host "[ ok ]  $m" -ForegroundColor Green }
function Write-Warn  { param($m) Write-Host "[warn]  $m" -ForegroundColor Yellow }
function Write-Fail  { param($m) Write-Host "[fail]  $m" -ForegroundColor Red }

# —— 帮助 —————————————————————————————————————————————————————————
function Show-Usage {
    @"
MossTerm 一键安装脚本 (Windows)

用法：install.ps1 [选项]

选项：
  -NoGo       跳过 Go
  -NoNode     跳过 Node.js
  -NoWails    跳过 Wails CLI
  -NoLint     跳过 golangci-lint
  -Help       显示本帮助

环境变量：
  GO_VERSION        默认 $GO_VERSION
  NODE_VERSION      默认 $NODE_VERSION
  WAILS_VERSION     默认 $WAILS_VERSION
"@
}

if ($Help) { Show-Usage; exit 0 }

# —— 包管理器 ————————————————————————————————————————————————————
function Get-PackageManager {
    if (Get-Command winget -ErrorAction SilentlyContinue) { return "winget" }
    if (Get-Command choco  -ErrorAction SilentlyContinue) { return "choco" }
    if (Get-Command scoop  -ErrorAction SilentlyContinue) { return "scoop" }
    return "none"
}

$PKG = Get-PackageManager
Write-Info "检测到包管理器：$($PKG)"

# —— 提权 —————————————————————————————————————————————————————————
function Test-IsAdmin {
    $id = New-Object Security.Principal.WindowsIdentity([Security.Principal.WindowsAccount]::Cur)
    $pr = New-Object Security.Principal.WindowsPrincipal($id)
    return $pr.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

# —— 安装：Go ————————————————————————————————————————————————————
function Install-Go {
    if ($NoGo) { return }
    if (Get-Command go -ErrorAction SilentlyContinue) {
        $v = (go version) -replace 'go version ', ''
        Write-Ok "Go $v 已安装"
        return
    }
    Write-Info "安装 Go $GO_VERSION ..."
    switch ($PKG) {
        "winget" { winget install --id GoLang.Go --silent --accept-source-agreements --accept-package-agreements }
        "choco"  { choco install -y golang }
        "scoop"  { scoop install go }
        default {
            Write-Warn "未识别包管理器；请手动安装 Go $GO_VERSION："
            Write-Warn "  https://go.dev/dl/"
            return
        }
    }
    Write-Ok "Go 已安装"
}

# —— 安装：Node.js ——————————————————————————————————————————————————
function Install-Node {
    if ($NoNode) { return }
    if (Get-Command node -ErrorAction SilentlyContinue) {
        $v = [int]((node -v) -replace 'v', '').Split('.')[0]
        if ($v -ge $NODE_VERSION) {
            Write-Ok "Node.js $(node -v) 已安装"
            return
        }
        Write-Warn "已安装 Node.js v$v，目标 v$NODE_VERSION+"
    }
    Write-Info "安装 Node.js $NODE_VERSION ..."
    switch ($PKG) {
        "winget" { winget install --id OpenJS.NodeJS.LTS --silent --accept-source-agreements --accept-package-agreements }
        "choco"  { choco install -y nodejs-lts }
        "scoop"  { scoop install nodejs-lts }
        default {
            Write-Warn "未识别包管理器；请手动安装 Node.js $NODE_VERSION+："
            Write-Warn "  https://nodejs.org/en/download/"
            return
        }
    }
    Write-Ok "Node.js 已安装"
}

# —— 安装：Wails CLI ——————————————————————————————————————————————————
function Install-Wails {
    if ($NoWails) { return }
    if (Get-Command wails -ErrorAction SilentlyContinue) {
        Write-Ok "wails 已安装"
        return
    }
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        Write-Fail "未检测到 go，无法安装 wails"
        return
    }
    Write-Info "通过 go install 安装 wails $WAILS_VERSION ..."
    $env:GO111MODULE = "on"
    go install "github.com/wailsapp/wails/v2/cmd/wails@$WAILS_VERSION"
    Write-Ok "wails 已安装到 $(go env GOPATH)\bin"
}

# —— 安装：golangci-lint ——————————————————————————————————————————————
function Install-GolangCI {
    if ($NoLint) { return }
    if (Get-Command golangci-lint -ErrorAction SilentlyContinue) {
        Write-Ok "golangci-lint 已安装"
        return
    }
    Write-Info "安装 golangci-lint $GOLANGCI_VERSION ..."
    $dest = Join-Path $env:USERPROFILE "go\bin"
    if (-not (Test-Path $dest)) { New-Item -ItemType Directory -Path $dest -Force | Out-Null }
    Invoke-Expression "$(Invoke-WebRequest -UseBasicParsing https://raw.githubusercontent.com/golangci/golangci-lint/master/install.ps1).Content" `
        | Invoke-Expression -ArgumentList @("-b", $dest, $GOLANGCI_VERSION)
    Write-Ok "golangci-lint 已安装到 $dest"
}

# —— 主流程 ————————————————————————————————————————————————————————
function Main {
    if (-not (Test-IsAdmin)) {
        Write-Warn "建议以管理员身份运行本脚本（部分安装需要 UAC）"
    }

    @"
  __  ___                  __    __                __
 /  |/  /__  ____ ___  ___/ /___/ /  ___  ___ ____/ /__ ____
/ /|_/ / _ \/ __/ -_) _  / __/ _ \/ _ \/ _ \`/ _  / -_) __/
              MossTerm installer (Windows) 🪴
  pkg mgr  : $PKG
"@ | Write-Host

    Install-Go
    Install-Node
    Install-Wails
    Install-GolangCI

    Write-Host ""
    Write-Ok "所有依赖安装完成。建议执行："
    Write-Host "    make deps-check"
    Write-Host "    make dev"
}

Main
