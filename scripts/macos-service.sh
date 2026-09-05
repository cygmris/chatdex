#!/bin/sh
# chatdex 的 macOS 用户级 launchd 管理。
# 不使用 root / sudo / system daemon。launchd 不展开 ~，本脚本只写绝对路径。
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
TEMPLATE="$REPO_ROOT/deploy/launchd/dev.cygmris.chatdex.plist"

CHATDEX_LABEL=${CHATDEX_LABEL:-dev.cygmris.chatdex}
HOME_DIR=${CHATDEX_HOME:-${HOME:?HOME is unset}}
CHATDEX_BIN=${CHATDEX_BIN:-$HOME_DIR/.local/bin/chatdex}
CHATDEX_CONFIG=${CHATDEX_CONFIG:-$HOME_DIR/.config/chatdex/config.json}
CHATDEX_DB=${CHATDEX_DB:-$HOME_DIR/.local/share/chatdex/index.db}
CHATDEX_PLIST=${CHATDEX_PLIST:-$HOME_DIR/Library/LaunchAgents/${CHATDEX_LABEL}.plist}
CHATDEX_LOG_DIR=${CHATDEX_LOG_DIR:-$HOME_DIR/Library/Logs/chatdex}
CHATDEX_PATH=${CHATDEX_PATH:-/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin}

usage() {
  cat <<'EOF'
用法: macos-service.sh <命令>

命令:
  install [--build]     生成 plist、bootstrap launchd，并等待 /api/health
  start                 bootstrap（若尚未载入）并等待健康检查
  stop                  bootout，并确认 launchctl 已卸载
  restart               stop + start
  status                launchd 状态 + 实际 HTTP /api/health（不只看 loaded）
  uninstall             停止并删除 plist 与二进制；保留配置、索引库、会话源文件
  upgrade [--build]     构建到 staging，原子替换二进制；失败则恢复旧二进制
  render-plist          把 plist 写到 stdout（Linux 上也可跑，供校验）

环境变量（测试可覆盖）:
  CHATDEX_HOME CHATDEX_LABEL CHATDEX_BIN CHATDEX_CONFIG CHATDEX_DB
  CHATDEX_PLIST CHATDEX_LOG_DIR CHATDEX_PATH CHATDEX_PREBUILT
EOF
}

die() { echo "macos-service.sh: $*" >&2; exit 1; }

require_abs() {
  case $1 in
    /*) ;;
    *) die "路径必须是绝对路径，不能依赖 ~ 展开: $1" ;;
  esac
  case $1 in
    *~*) die "路径含 ~，launchd 不会展开: $1" ;;
  esac
}

require_darwin() {
  [ "$(uname -s)" = Darwin ] || die "这条命令只在 macOS 上跑（当前是 $(uname -s)）"
}

uid_num() { id -u; }

launch_domain() {
  u=$(uid_num)
  if launchctl print "gui/${u}" >/dev/null 2>&1; then
    echo "gui/${u}"
    return
  fi
  if launchctl print "user/${u}" >/dev/null 2>&1; then
    echo "user/${u}"
    return
  fi
  echo "gui/${u}"
}

is_loaded() {
  launchctl print "$(launch_domain)/$CHATDEX_LABEL" >/dev/null 2>&1
}

ensure_dirs() {
  require_abs "$HOME_DIR"
  require_abs "$CHATDEX_BIN"
  require_abs "$CHATDEX_CONFIG"
  require_abs "$CHATDEX_DB"
  require_abs "$CHATDEX_PLIST"
  require_abs "$CHATDEX_LOG_DIR"
  mkdir -p -m 755 "$(dirname "$CHATDEX_BIN")"
  mkdir -p -m 700 "$(dirname "$CHATDEX_CONFIG")"
  mkdir -p -m 700 "$(dirname "$CHATDEX_DB")"
  mkdir -p -m 700 "$CHATDEX_LOG_DIR"
  mkdir -p -m 755 "$(dirname "$CHATDEX_PLIST")"
}

render_plist() {
  [ -f "$TEMPLATE" ] || die "找不到 plist 模板 $TEMPLATE"
  require_abs "$CHATDEX_BIN"
  require_abs "$CHATDEX_CONFIG"
  require_abs "$HOME_DIR"
  require_abs "$CHATDEX_LOG_DIR"
  out=$(sed \
    -e "s|__LABEL__|$CHATDEX_LABEL|g" \
    -e "s|__BINARY__|$CHATDEX_BIN|g" \
    -e "s|__CONFIG__|$CHATDEX_CONFIG|g" \
    -e "s|__HOME__|$HOME_DIR|g" \
    -e "s|__LOG_DIR__|$CHATDEX_LOG_DIR|g" \
    -e "s|__PATH__|$CHATDEX_PATH|g" \
    "$TEMPLATE")
  case $out in
    *~*) die "生成的 plist 仍含 ~" ;;
  esac
  printf '%s\n' "$out"
}

write_plist() {
  ensure_dirs
  render_plist >"$CHATDEX_PLIST"
  chmod 644 "$CHATDEX_PLIST"
}

api_port() {
  if [ -f "$CHATDEX_CONFIG" ]; then
    python3 -c 'import json,sys
p=sys.argv[1]
try:
  c=json.load(open(p))
except Exception:
  print(5022); raise SystemExit
print(int((c.get("ports") or {}).get("api") or 5022))' "$CHATDEX_CONFIG"
  else
    echo 5022
  fi
}

health_url() { echo "http://127.0.0.1:$(api_port)/api/health"; }

wait_health() {
  url=$(health_url)
  i=0
  while [ "$i" -lt 30 ]; do
    if body=$(curl -fsS "$url" 2>/dev/null); then
      printf '%s\n' "$body"
      echo "$body" | grep -q '"ok":true' || die "健康检查返回了非 ok: $body"
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  echo "30s 内 $url 不可访问" >&2
  if [ -f "$CHATDEX_LOG_DIR/stderr.log" ]; then
    echo "--- stderr.log ---" >&2
    tail -20 "$CHATDEX_LOG_DIR/stderr.log" >&2 || true
  fi
  return 1
}

verify_binary() {
  bin=$1
  [ -f "$bin" ] || die "二进制不存在: $bin"
  [ -x "$bin" ] || die "二进制不可执行: $bin"
  ft=$(file -b "$bin")
  echo "file: $ft"
  echo "$ft" | grep -q Mach-O || die "不是 Mach-O: $ft"
  echo "$ft" | grep -qi executable || die "不是 executable: $ft"
  arch=$(uname -m)
  echo "$ft" | grep -q "$arch" || die "架构对不上，当前 $(uname -m)，file 输出: $ft"
  shasum -a 256 "$bin"
}

build_staging() {
  [ -n "${CHATDEX_PREBUILT:-}" ] && { echo "$CHATDEX_PREBUILT"; return; }
  command -v go >/dev/null || die "未找到 go，且未设置 CHATDEX_PREBUILT"
  staging=$(mktemp -d "${TMPDIR:-/tmp}/chatdex-stage.XXXXXX")/chatdex
  ver=$(git -C "$REPO_ROOT" describe --tags --always --dirty 2>/dev/null || echo dev)
  commit=$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo)
  echo "构建 $ver ($commit) → $staging" >&2
  (cd "$REPO_ROOT" && CGO_ENABLED=0 go build \
    -ldflags "-X github.com/cygmris/chatdex/internal/version.Version=${ver} -X github.com/cygmris/chatdex/internal/version.Commit=${commit}" \
    -o "$staging" ./cmd/chatdex)
  echo "$staging"
}

atomic_install_bin() {
  src=$1
  dest=$CHATDEX_BIN
  ensure_dirs
  tmp=$dest.new
  # BSD install 在目标是正在跑的 Mach-O 时会 ETXTBSY；调用方必须先 stop。
  /usr/bin/install -m 755 "$src" "$tmp"
  mv -f "$tmp" "$dest"
}

restore_backup_or_die() {
  msg=$1
  echo "$msg" >&2
  if [ -f "$CHATDEX_BIN.bak" ]; then
    atomic_install_bin "$CHATDEX_BIN.bak"
    cmd_start || die "回滚后仍然起不来"
    die "已恢复上一个二进制"
  fi
  die "没有可回滚的旧二进制"
}

cmd_stop() {
  require_darwin
  if is_loaded; then
    domain=$(launch_domain)
    launchctl bootout "$domain/$CHATDEX_LABEL" 2>/dev/null || \
      launchctl bootout "$domain" "$CHATDEX_PLIST" 2>/dev/null || true
  fi
  i=0
  while is_loaded && [ "$i" -lt 15 ]; do
    sleep 1
    i=$((i + 1))
  done
  is_loaded && die "bootout 之后 launchctl 仍显示已载入"
  echo "已停止 $CHATDEX_LABEL"
}

cmd_start() {
  require_darwin
  [ -x "$CHATDEX_BIN" ] || die "没有可执行文件 $CHATDEX_BIN"
  ensure_dirs
  write_plist
  if is_loaded; then
    echo "已在运行，先 bootout 再 bootstrap"
    cmd_stop
  fi
  launchctl bootstrap "$(launch_domain)" "$CHATDEX_PLIST"
  wait_health
}

cmd_install() {
  require_darwin
  do_build=0
  [ "${1:-}" = --build ] && do_build=1
  ensure_dirs
  if [ "$do_build" -eq 1 ]; then
    staging=$(build_staging)
    verify_binary "$staging"
    if is_loaded; then
      cmd_stop
    fi
    if [ -f "$CHATDEX_BIN" ]; then
      cp -p "$CHATDEX_BIN" "$CHATDEX_BIN.bak"
    fi
    atomic_install_bin "$staging"
  else
    if [ -n "${CHATDEX_PREBUILT:-}" ]; then
      verify_binary "$CHATDEX_PREBUILT"
      if is_loaded; then
        cmd_stop
      fi
      if [ -f "$CHATDEX_BIN" ]; then
        cp -p "$CHATDEX_BIN" "$CHATDEX_BIN.bak"
      fi
      atomic_install_bin "$CHATDEX_PREBUILT"
    fi
  fi
  [ -x "$CHATDEX_BIN" ] || die "install 需要已有二进制，或加 --build / CHATDEX_PREBUILT"
  # 不创建、不覆盖已有 config.json 与 index.db
  cmd_start || restore_backup_or_die "启动失败"
}

cmd_restart() {
  cmd_stop
  cmd_start
}

cmd_status() {
  require_darwin
  echo "label    $CHATDEX_LABEL"
  echo "domain   $(launch_domain)"
  echo "binary   $CHATDEX_BIN"
  echo "config   $CHATDEX_CONFIG"
  echo "index    $CHATDEX_DB"
  echo "plist    $CHATDEX_PLIST"
  echo "logs     $CHATDEX_LOG_DIR"
  if [ -x "$CHATDEX_BIN" ]; then
    echo "version  $($CHATDEX_BIN version 2>/dev/null || echo unknown)"
  else
    echo "version  (no binary)"
  fi
  if is_loaded; then
    echo "launchd  loaded"
  else
    echo "launchd  not loaded"
  fi
  url=$(health_url)
  if body=$(curl -fsS "$url" 2>/dev/null); then
    echo "health   $url"
    printf '%s\n' "$body"
    echo "$body" | grep -q '"ok":true' || exit 1
  else
    echo "health   $url  unreachable"
    is_loaded && exit 1
    exit 1
  fi
}

cmd_uninstall() {
  require_darwin
  cmd_stop || true
  rm -f "$CHATDEX_PLIST"
  rm -f "$CHATDEX_BIN" "$CHATDEX_BIN.bak" "$CHATDEX_BIN.new"
  echo "已卸载服务与二进制。"
  echo "保留：配置 $CHATDEX_CONFIG 、索引库 $CHATDEX_DB 、原始会话文件、日志 $CHATDEX_LOG_DIR"
}

cmd_upgrade() {
  require_darwin
  do_build=1
  [ "${1:-}" = --prebuilt ] && do_build=0
  if [ "$do_build" -eq 1 ]; then
    staging=$(build_staging)
  else
    [ -n "${CHATDEX_PREBUILT:-}" ] || die "upgrade --prebuilt 需要 CHATDEX_PREBUILT"
    staging=$CHATDEX_PREBUILT
  fi
  verify_binary "$staging"
  cmd_stop || true
  if [ -f "$CHATDEX_BIN" ]; then
    cp -p "$CHATDEX_BIN" "$CHATDEX_BIN.bak"
  fi
  atomic_install_bin "$staging"
  if cmd_start; then
    echo "升级成功"
    return 0
  fi
  restore_backup_or_die "升级后健康检查失败"
}

cmd=${1:-}
[ -n "$cmd" ] || { usage; exit 2; }
shift || true
case $cmd in
  install) cmd_install "${1:-}" ;;
  start) cmd_start ;;
  stop) cmd_stop ;;
  restart) cmd_restart ;;
  status) cmd_status ;;
  uninstall) cmd_uninstall ;;
  upgrade) cmd_upgrade "${1:-}" ;;
  render-plist) render_plist ;;
  -h|--help|help) usage ;;
  *) usage; die "未知命令 $cmd" ;;
esac
