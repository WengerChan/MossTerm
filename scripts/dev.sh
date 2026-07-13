#!/usr/bin/env bash
# =============================================================================
# MossTerm 开发模式启动脚本
# -----------------------------------------------------------------------------
# 行为：
#   - 默认执行 `wails dev`（HMR + Go 自动重编）
#   - --backend-only   仅启动后端 `go run`（方便纯后端联调，无 webview）
#   - --frontend-only  仅启动前端 vite dev server（前后端分离开发）
#   - --no-frontend    不安装/更新前端依赖
#   - --wails-flags=… 透传给 wails dev
#
# 用法：
#   ./scripts/dev.sh
#   ./scripts/dev.sh --backend-only
#   ./scripts/dev.sh --wails-flags="-browser"
# =============================================================================
set -euo pipefail

# —— 路径 ————————————————————————————————————————————————————————————
ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FRONTEND_DIR="${ROOT_DIR}/frontend"
cd "${ROOT_DIR}"

# —— 颜色 ————————————————————————————————————————————————————————————
if [ -t 1 ]; then
  C_BLUE='\033[0;34m'; C_GREEN='\033[0;32m'; C_YELLOW='\033[0;33m'; C_RED='\033[0;31m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_RESET=''
fi
info() { printf "${C_BLUE}[dev]${C_RESET}  %s\n" "$*"; }
ok()   { printf "${C_GREEN}[ok]${C_RESET}   %s\n" "$*"; }
warn() { printf "${C_YELLOW}[warn]${C_RESET} %s\n" "$*"; }
err()  { printf "${C_RED}[fail]${C_RESET} %s\n" "$*" >&2; }

# —— 参数 ————————————————————————————————————————————————————————————
BACKEND_ONLY=0
FRONTEND_ONLY=0
SKIP_FRONTEND_DEPS=0
WAILS_FLAGS=""

usage() {
  cat <<USAGE
MossTerm 开发模式启动脚本

用法：${0##*/} [选项]

选项：
  --backend-only       仅运行 Go 后端
  --frontend-only      仅启动前端 vite dev server
  --no-frontend        跳过前端依赖安装
  --wails-flags="…"    透传给 wails dev
  -h, --help           显示帮助
USAGE
}

while [ $# -gt 0 ]; do
  case "$1" in
    --backend-only)      BACKEND_ONLY=1 ;;
    --frontend-only)     FRONTEND_ONLY=1 ;;
    --no-frontend)       SKIP_FRONTEND_DEPS=1 ;;
    --wails-flags=*)     WAILS_FLAGS="${1#*=}" ;;
    -h|--help)           usage; exit 0 ;;
    *) err "未知参数：$1"; usage; exit 1 ;;
  esac
  shift
done

# —— 工具 ————————————————————————————————————————————————————————————
have() { command -v "$1" >/dev/null 2>&1; }

require_go() {
  if ! have go; then err "未检测到 go，请先安装 Go 1.22+"; exit 1; fi
}

require_wails() {
  if ! have wails; then
    err "未检测到 wails CLI；执行：go install github.com/wailsapp/wails/v2/cmd/wails@v2.9.2"
    exit 1
  fi
}

require_node() {
  if ! have node; then err "未检测到 node；请安装 Node 18+"; exit 1; fi
}

# —— 同步前端依赖 ————————————————————————————————————————————————
sync_frontend_deps() {
  if [ "${SKIP_FRONTEND_DEPS}" = "1" ]; then
    info "跳过前端依赖同步（--no-frontend）"
    return
  fi
  if [ ! -d "${FRONTEND_DIR}" ]; then
    warn "frontend/ 目录不存在，跳过依赖同步"
    return
  fi
  if [ ! -f "${FRONTEND_DIR}/package.json" ]; then
    warn "frontend/package.json 不存在，跳过依赖同步"
    return
  fi

  info "同步前端依赖..."
  pushd "${FRONTEND_DIR}" >/dev/null
  if [ -f package-lock.json ]; then
    npm ci
  elif [ -f pnpm-lock.yaml ] && have pnpm; then
    pnpm install --frozen-lockfile
  elif [ -f yarn.lock ] && have yarn; then
    yarn install --frozen-lockfile
  else
    npm install
  fi
  popd >/dev/null
  ok "前端依赖就绪"
}

# —— Wails doctor（开发前自检） ————————————————————————————————
run_wails_doctor() {
  if have wails; then
    info "执行 wails doctor 自检..."
    if ! wails doctor; then
      warn "wails doctor 报告了问题，可忽略（取决于环境）"
    fi
  fi
}

# —— 主流程 ————————————————————————————————————————————————————————
main() {
  cat <<BANNER
  __  ___                  __    __                __
 /  |/  /__  ____ ___  ___/ /___/ /  ___  ___ ____/ /__ ____
/ /|_/ / _ \/ __/ -_) _  / __/ _ \/ _ \/ _ \`/ _  / -_) __/
              MossTerm dev mode 🪴
  root     : ${ROOT_DIR}
BANNER
  echo

  require_go

  if [ "${BACKEND_ONLY}" = "1" ]; then
    info "backend-only 模式：go run ./cmd/mossterm"
    exec go run ./cmd/mossterm
  fi

  if [ "${FRONTEND_ONLY}" = "1" ]; then
    require_node
    sync_frontend_deps
    info "frontend-only 模式：vite dev"
    pushd "${FRONTEND_DIR}" >/dev/null
    if grep -q '"dev"' package.json; then
      exec npm run dev
    else
      err "frontend/package.json 没有 dev script"
      exit 1
    fi
  fi

  # 默认：wails dev
  require_wails
  require_node
  sync_frontend_deps
  run_wails_doctor

  if [ -n "${WAILS_FLAGS}" ]; then
    info "启动 wails dev（flags: ${WAILS_FLAGS}）"
    # shellcheck disable=SC2086
    exec wails dev ${WAILS_FLAGS}
  else
    info "启动 wails dev"
    exec wails dev
  fi
}

main "$@"
