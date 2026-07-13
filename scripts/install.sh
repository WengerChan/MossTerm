#!/usr/bin/env bash
# =============================================================================
# MossTerm 一键安装依赖（Linux / macOS）
# -----------------------------------------------------------------------------
# 安装：
#   - Go（如果缺失且系统无包管理器则提示）
#   - Node.js 20.x + npm（如果缺失）
#   - Wails CLI v2
#   - golangci-lint（如果缺失）
#   - 平台构建依赖（libgtk、libwebkit 等，Linux 需要）
#
# 用法：
#   ./scripts/install.sh                 # 默认：全部安装
#   ./scripts/install.sh --no-go         # 跳过 Go
#   ./scripts/install.sh --no-wails      # 跳过 wails CLI
#   ./scripts/install.sh --with-brew     # 强制用 Homebrew
#   ./scripts/install.sh --help
# =============================================================================
set -euo pipefail

# —— 工具与版本 ————————————————————————————————————————————————————————
GO_VERSION="${GO_VERSION:-1.22.0}"
NODE_VERSION="${NODE_VERSION:-20}"
WAILS_VERSION="${WAILS_VERSION:-v2.9.2}"
GOLANGCI_VERSION="${GOLANGCI_VERSION:-latest}"

# —— 平台识别 ————————————————————————————————————————————————————————
OS="$(uname -s)"
case "${OS}" in
  Linux*)  PLATFORM=linux  ;;
  Darwin*) PLATFORM=darwin ;;
  *) echo "不支持的操作系统：${OS}"; exit 1 ;;
esac
ARCH="$(uname -m)"
case "${ARCH}" in
  x86_64|amd64)   GO_ARCH=amd64 ;;
  arm64|aarch64)  GO_ARCH=arm64 ;;
  *) echo "不支持的架构：${ARCH}"; exit 1 ;;
esac

# —— 颜色 ————————————————————————————————————————————————————————————
if [ -t 1 ]; then
  C_RED='\033[0;31m'; C_GREEN='\033[0;32m'; C_YELLOW='\033[0;33m'
  C_BLUE='\033[0;34m'; C_RESET='\033[0m'
else
  C_RED=''; C_GREEN=''; C_YELLOW=''; C_BLUE=''; C_RESET=''
fi

info()  { printf "${C_BLUE}[info]${C_RESET}  %s\n" "$*"; }
ok()    { printf "${C_GREEN}[ ok ]${C_RESET}  %s\n" "$*"; }
warn()  { printf "${C_YELLOW}[warn]${C_RESET}  %s\n" "$*"; }
err()   { printf "${C_RED}[fail]${C_RESET} %s\n" "$*" >&2; }

# —— 参数解析 ————————————————————————————————————————————————————————
INSTALL_GO=1
INSTALL_NODE=1
INSTALL_WAILS=1
INSTALL_LINT=1
USE_BREW=0

usage() {
  cat <<USAGE
MossTerm 一键安装脚本

用法：${0##*/} [选项]

选项：
  --no-go           跳过 Go
  --no-node         跳过 Node.js
  --no-wails        跳过 Wails CLI
  --no-lint         跳过 golangci-lint
  --with-brew       强制使用 Homebrew（macOS）
  -h, --help        显示本帮助

环境变量：
  GO_VERSION        默认 ${GO_VERSION}
  NODE_VERSION      默认 ${NODE_VERSION}
  WAILS_VERSION     默认 ${WAILS_VERSION}
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --no-go)      INSTALL_GO=0 ;;
    --no-node)    INSTALL_NODE=0 ;;
    --no-wails)   INSTALL_WAILS=0 ;;
    --no-lint)    INSTALL_LINT=0 ;;
    --with-brew)  USE_BREW=1 ;;
    -h|--help)    usage; exit 0 ;;
    *) err "未知参数：$1"; usage; exit 1 ;;
  esac
  shift
done

# —— 检测已有 ————————————————————————————————————————————————————————
have() { command -v "$1" >/dev/null 2>&1; }

# —— macOS / Linux 包管理器 ——————————————————————————————————————————
detect_pkg_manager() {
  if [ "${PLATFORM}" = "darwin" ]; then
    if have brew; then echo brew; return; fi
    echo none; return
  fi
  if have apt-get; then echo apt; return; fi
  if have dnf;     then echo dnf; return; fi
  if have yum;     then echo yum; return; fi
  if have apk;     then echo apk; return; fi
  if have pacman;  then echo pacman; return; fi
  echo none
}

PKG="$(detect_pkg_manager)"
info "检测到包管理器：${PKG:-<none>}"

# —— sudo ————————————————————————————————————————————————————————————
sudo_cmd() {
  if [ "$(id -u)" -eq 0 ]; then
    "$@"
  elif have sudo; then
    sudo "$@"
  else
    err "需要 root 权限来执行 $*，但找不到 sudo"; exit 1
  fi
}

# —— 安装：Go ————————————————————————————————————————————————————
install_go() {
  if have go && [ "${INSTALL_GO}" = "1" ]; then
    local current
    current="$(go version | awk '{print $3}' | sed 's/go//')"
    if [ "${current}" = "${GO_VERSION}" ]; then
      ok "Go ${current} 已安装"; return
    fi
    warn "已安装 Go ${current}，目标 ${GO_VERSION}；如需升级请手动处理"
    return
  fi

  case "${PKG}" in
    brew)
      brew install "go@${GO_VERSION}" || brew install go
      ;;
    apt|dnf|yum|apk|pacman)
      warn "系统包管理器的 Go 版本可能偏旧；推荐手动安装官方版本"
      case "${PKG}" in
        apt)    sudo_cmd apt-get update && sudo_cmd apt-get install -y golang-go ;;
        dnf)    sudo_cmd dnf install -y golang ;;
        yum)    sudo_cmd yum install -y golang ;;
        apk)    sudo_cmd apk add --no-cache go ;;
        pacman) sudo_cmd pacman -Sy --noconfirm go ;;
      esac
      ;;
    none)
      info "下载 Go ${GO_VERSION} 二进制..."
      local tarball="go${GO_VERSION}.${PLATFORM}-${GO_ARCH}.tar.gz"
      local url="https://go.dev/dl/${tarball}"
      local dest="/usr/local/go"
      curl -fsSL -o "/tmp/${tarball}" "${url}"
      sudo_cmd rm -rf "${dest}"
      sudo_cmd tar -C /usr/local -xzf "/tmp/${tarball}"
      rm -f "/tmp/${tarball}"
      ok "Go 已安装到 ${dest}（请确保 PATH 包含 /usr/local/go/bin）"
      ;;
  esac
}

# —— 安装：Node.js ——————————————————————————————————————————————————
install_node() {
  if have node; then
    local v
    v="$(node -v | sed 's/v//' | cut -d. -f1)"
    if [ "${v}" -ge "${NODE_VERSION}" ]; then
      ok "Node.js $(node -v) 已安装"; return
    fi
    warn "已安装 Node.js v${v}，目标 v${NODE_VERSION}+"
  fi

  case "${PKG}" in
    brew)
      brew install "node@${NODE_VERSION}" || brew install node
      ;;
    apt|dnf|yum|apk|pacman)
      warn "推荐用 nvm 装 Node（系统包版本可能偏旧）"
      case "${PKG}" in
        apt)    sudo_cmd apt-get install -y nodejs npm ;;
        dnf)    sudo_cmd dnf install -y nodejs npm ;;
        yum)    sudo_cmd yum install -y nodejs npm ;;
        apk)    sudo_cmd apk add --no-cache nodejs npm ;;
        pacman) sudo_cmd pacman -Sy --noconfirm nodejs npm ;;
      esac
      ;;
    none)
      warn "未识别包管理器；请手动安装 Node.js ${NODE_VERSION}+："
      warn "  https://nodejs.org/en/download/"
      ;;
  esac
}

# —— 安装：Wails CLI ——————————————————————————————————————————————————
install_wails() {
  if have wails; then
    ok "wails $(wails version 2>/dev/null || echo unknown) 已安装"; return
  fi

  info "通过 go install 安装 wails ${WAILS_VERSION}..."
  GOBIN="${GOBIN:-$(go env GOPATH 2>/dev/null)/bin}" go install \
    "github.com/wailsapp/wails/v2/cmd/wails@${WAILS_VERSION}"
  ok "wails 已安装到 ${GOBIN:-$(go env GOPATH)/bin}"
}

# —— 安装：golangci-lint ——————————————————————————————————————————————
install_golangci() {
  if have golangci-lint; then
    ok "golangci-lint $(golangci-lint --version 2>/dev/null | awk '{print $4}') 已安装"; return
  fi

  info "安装 golangci-lint ${GOLANGCI_VERSION}..."
  if [ "${PLATFORM}" = "darwin" ]; then
    if [ "${GO_ARCH}" = "arm64" ]; then arch=arm64; else arch=amd64; fi
  else
    if [ "${GO_ARCH}" = "arm64" ]; then arch=arm64; else arch=amd64; fi
  fi
  local bin="/usr/local/bin/golangci-lint"
  curl -fsSL "https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh" \
    | sh -s -- -b "$(dirname "${bin}")" "${GOLANGCI_VERSION}"
  ok "golangci-lint 已安装到 ${bin}"
}

# —— Linux GTK/WebKit 依赖（仅 Linux） ————————————————————————————————
install_linux_build_deps() {
  if [ "${PLATFORM}" != "linux" ]; then return; fi
  if [ "${PKG}" = "none" ]; then
    warn "未识别 Linux 发行版；请手动安装 libgtk-3-dev / libwebkit2gtk-4.0-dev / pkg-config"
    return
  fi
  info "安装 Linux 桌面构建依赖（libgtk / webkit2gtk）..."
  case "${PKG}" in
    apt)
      sudo_cmd apt-get install -y \
        build-essential pkg-config \
        libgtk-3-dev libwebkit2gtk-4.0-dev
      ;;
    dnf|yum)
      sudo_cmd "${PKG}" install -y \
        gcc pkgconf-pkg-config gtk3-devel webkit2gtk4.0-devel
      ;;
    apk)
      sudo_cmd apk add --no-cache \
        build-base pkgconfig gtk+3.0-dev webkit2gtk-4.0-dev
      ;;
    pacman)
      sudo_cmd pacman -Sy --noconfirm \
        base-devel pkgconf gtk3 webkit2gtk
      ;;
  esac
  ok "Linux 构建依赖已就绪"
}

# —— 主流程 ————————————————————————————————————————————————————————
main() {
  cat <<BANNER
  __  ___                  __    __                __
 /  |/  /__  ____ ___  ___/ /___/ /  ___  ___ ____/ /__ ____
/ /|_/ / _ \/ __/ -_) _  / __/ _ \/ _ \/ _ \`/ _  / -_) __/
              MossTerm installer 🪴
  platform : ${PLATFORM}/${GO_ARCH}
  pkg mgr  : ${PKG:-<none>}
BANNER
  echo

  if [ "${INSTALL_GO}" = "1" ];    then install_go; fi
  if [ "${INSTALL_NODE}" = "1" ];  then install_node; fi
  install_linux_build_deps
  if [ "${INSTALL_WAILS}" = "1" ]; then install_wails; fi
  if [ "${INSTALL_LINT}" = "1" ];  then install_golangci; fi

  echo
  ok "所有依赖安装完成。建议执行："
  echo "    make deps-check"
  echo "    make dev"
}

main "$@"
