#!/usr/bin/env bash
# =============================================================================
# MossTerm 跨平台构建脚本
# -----------------------------------------------------------------------------
# 行为：
#   - 默认构建当前平台的二进制
#   - --all           构建 darwin/linux/windows 三平台六架构
#   - --platform=X    单平台构建（逗号分隔），如 darwin/amd64,linux/arm64
#   - --out=DIR       产物输出目录（默认 dist/）
#   - --clean         清理后再构建
#   - --no-zip        不打包压缩
#   - --docker        在 docker 容器内构建（Linux 跨平台避免 glibc 兼容问题）
#
# 依赖：go 1.22+；macOS 还需 xcrun；Linux 需 CGO 工具链
# =============================================================================
set -euo pipefail

# —— 路径与默认值 ————————————————————————————————————————————————
APP="mossterm"
PKG="github.com/mossterm/${APP}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "dev")}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo "none")}"
BUILD_DATE="${BUILD_DATE:-$(date -u +"%Y-%m-%dT%H:%M:%SZ")}"

LDFLAGS="-s -w \
  -X '${PKG}/internal/app.Version=${VERSION}' \
  -X '${PKG}/internal/app.Commit=${COMMIT}' \
  -X '${PKG}/internal/app.BuildDate=${BUILD_DATE}'"

OUT_DIR="dist"
BUILD_ALL=0
BUILD_PLATFORMS=""
DO_CLEAN=0
DO_ZIP=1
USE_DOCKER=0

# —— 平台矩阵 ————————————————————————————————————————————————————
DEFAULT_PLATFORMS=(
  "darwin/amd64"
  "darwin/arm64"
  "linux/amd64"
  "linux/arm64"
  "windows/amd64"
  "windows/arm64"
)

# —— 颜色 ————————————————————————————————————————————————————————————
if [ -t 1 ]; then
  C_BLUE='\033[0;34m'; C_GREEN='\033[0;32m'; C_RED='\033[0;31m'; C_RESET='\033[0m'
else
  C_BLUE=''; C_GREEN=''; C_RED=''; C_RESET=''
fi
info() { printf "${C_BLUE}[build]${C_RESET} %s\n" "$*"; }
ok()   { printf "${C_GREEN}[ ok  ]${C_RESET} %s\n" "$*"; }
err()  { printf "${C_RED}[fail ]${C_RESET} %s\n" "$*" >&2; }

usage() {
  cat <<USAGE
MossTerm 跨平台构建脚本

用法：${0##*/} [选项]

选项：
  --all                 构建全部平台（darwin / linux / windows 全架构）
  --platform=OS/ARCH    单平台构建（可重复或逗号分隔）
                        例：--platform=darwin/arm64,linux/amd64
  --out=DIR             产物输出目录（默认 dist/）
  --clean               清理后再构建
  --no-zip              不打包归档
  --docker              在 docker 容器内构建（Linux 全静态或避免 glibc 差异）
  -h, --help            显示本帮助

环境变量：
  VERSION, COMMIT, BUILD_DATE
USAGE
}

# —— 参数解析 ————————————————————————————————————————————————————————
while [ $# -gt 0 ]; do
  case "$1" in
    --all)              BUILD_ALL=1 ;;
    --platform=*)       IFS=',' read -ra BUILD_PLATFORMS <<< "${1#*=}" ;;
    --out=*)            OUT_DIR="${1#*=}" ;;
    --clean)            DO_CLEAN=1 ;;
    --no-zip)           DO_ZIP=0 ;;
    --docker)           USE_DOCKER=1 ;;
    -h|--help)          usage; exit 0 ;;
    *) err "未知参数：$1"; usage; exit 1 ;;
  esac
  shift
done

# —— 工具 ————————————————————————————————————————————————————————————
have() { command -v "$1" >/dev/null 2>&1; }

require_go() {
  if ! have go; then
    err "未检测到 go，请先安装 Go 1.22+"; exit 1
  fi
  info "使用 $(go version)"
}

# —— 单平台构建 ——————————————————————————————————————————————————
build_one() {
  local platform="$1"
  local goos="${platform%/*}"
  local goarch="${platform#*/}"
  local ext=""
  if [ "${goos}" = "windows" ]; then ext=".exe"; fi

  local out="${OUT_DIR}/${goos}/${goarch}/${APP}${ext}"
  mkdir -p "$(dirname "${out}")"

  info "[${goos}/${goarch}] 构建中..."
  GOOS="${goos}" GOARCH="${goarch}" CGO_ENABLED=1 \
    go build -trimpath -ldflags "${LDFLAGS}" \
      -o "${out}" "./cmd/${APP}"
  ok "[${goos}/${goarch}] -> ${out}"

  if [ "${DO_ZIP}" = "1" ]; then
    local archive="${OUT_DIR}/${APP}-${VERSION}-${goos}-${goarch}"
    if [ "${goos}" = "windows" ]; then
      (cd "$(dirname "${out}")" && zip -q "$(basename "${archive}").zip" "$(basename "${out}")")
      ok "[${goos}/${goarch}] -> ${archive}.zip"
    else
      tar -C "$(dirname "${out}")" -czf "${archive}.tar.gz" "$(basename "${out}")"
      ok "[${goos}/${goarch}] -> ${archive}.tar.gz"
    fi
  fi
}

# —— Docker 模式（Linux 全平台静态构建） ————————————————————————
build_in_docker() {
  local platform="$1"
  local goos="${platform%/*}"
  local goarch="${platform#*/}"
  if [ "${goos}" != "linux" ]; then
    err "docker 模式仅支持 linux/*，其它平台请用原生工具链"
    return 1
  fi
  info "[docker linux/${goarch}] 构建中..."
  docker run --rm \
    -v "$(pwd):/src" -w /src \
    -e GOOS=linux -e GOARCH="${goarch}" -e CGO_ENABLED=1 \
    golang:1.22 \
    bash -c "apt-get update && apt-get install -y gcc libc6-dev && go build -trimpath -ldflags '${LDFLAGS}' -o '${OUT_DIR}/linux/${goarch}/${APP}' ./cmd/${APP}"
  ok "[docker linux/${goarch}] 完成"
}

# —— 校验平台格式 ————————————————————————————————————————————————
validate_platform() {
  local p="$1"
  if ! [[ "$p" =~ ^(darwin|linux|windows)/(amd64|arm64)$ ]]; then
    err "非法平台：$p（应为 OS/ARCH，如 darwin/arm64）"; exit 1
  fi
}

# —— 主流程 ————————————————————————————————————————————————————————
main() {
  echo
  echo "  MossTerm build 🪴"
  echo "  version  : ${VERSION}"
  echo "  commit   : ${COMMIT}"
  echo "  date     : ${BUILD_DATE}"
  echo "  out dir  : ${OUT_DIR}"
  echo

  require_go

  if [ "${DO_CLEAN}" = "1" ]; then
    info "清理 ${OUT_DIR}/"
    rm -rf "${OUT_DIR}"
  fi
  mkdir -p "${OUT_DIR}"

  # 决定构建列表
  local -a PLATFORMS=()
  if [ "${BUILD_ALL}" = "1" ]; then
    PLATFORMS=("${DEFAULT_PLATFORMS[@]}")
  elif [ "${#BUILD_PLATFORMS[@]}" -gt 0 ]; then
    PLATFORMS=("${BUILD_PLATFORMS[@]}")
    for p in "${PLATFORMS[@]}"; do validate_platform "$p"; done
  else
    local host_os host_arch
    host_os="$(go env GOOS)"
    host_arch="$(go env GOARCH)"
    PLATFORMS=("${host_os}/${host_arch}")
  fi

  info "目标平台：${PLATFORMS[*]}"

  for p in "${PLATFORMS[@]}"; do
    if [ "${USE_DOCKER}" = "1" ]; then
      build_in_docker "$p"
    else
      build_one "$p"
    fi
  done

  echo
  ok "构建完成，产物在 ${OUT_DIR}/"
  find "${OUT_DIR}" -maxdepth 3 -type f \( -name "${APP}*" -o -name "*.zip" -o -name "*.tar.gz" \) | sort
}

main "$@"
