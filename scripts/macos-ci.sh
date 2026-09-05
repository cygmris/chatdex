#!/bin/sh
# 在隔离前缀下跑 macOS 安装 / 健康检查 / 升级。不碰真实用户的会话目录。
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
SERVICE="$SCRIPT_DIR/macos-service.sh"

assert_no_tilde() {
  case $1 in
    *~*) echo "FAIL: 输出含 ~: $1" >&2; exit 1 ;;
  esac
}

echo "== render-plist =="
export CHATDEX_HOME=/tmp/chatdex-plist-check
export CHATDEX_LABEL=dev.cygmris.chatdex.ci
export CHATDEX_BIN=/tmp/chatdex-plist-check/.local/bin/chatdex
export CHATDEX_CONFIG=/tmp/chatdex-plist-check/.config/chatdex/config.json
export CHATDEX_DB=/tmp/chatdex-plist-check/.local/share/chatdex/index.db
export CHATDEX_PLIST=/tmp/chatdex-plist-check/Library/LaunchAgents/dev.cygmris.chatdex.ci.plist
export CHATDEX_LOG_DIR=/tmp/chatdex-plist-check/Library/Logs/chatdex
plist=$("$SERVICE" render-plist)
assert_no_tilde "$plist"
echo "$plist" | grep -q -- '--config' || { echo "FAIL: plist 没有 --config"; exit 1; }
echo "$plist" | grep -q RunAtLoad || { echo "FAIL: 缺少 RunAtLoad"; exit 1; }
echo "$plist" | grep -q KeepAlive || { echo "FAIL: 缺少 KeepAlive"; exit 1; }
echo "$plist" | grep -q ThrottleInterval || { echo "FAIL: 缺少 ThrottleInterval"; exit 1; }
echo "$plist" | grep -q 127.0.0.1 && { echo "FAIL: plist 不该写死监听地址（由程序保证）"; exit 1; }
echo "$plist" | grep -q '/usr/bin:/bin:/usr/sbin:/sbin' || true
echo "render-plist OK"

if [ "$(uname -s)" != Darwin ]; then
  echo "非 Darwin，跳过 launchd / plutil / 安装流程"
  exit 0
fi

if command -v plutil >/dev/null; then
  tmp=$(mktemp)
  printf '%s\n' "$plist" >"$tmp"
  plutil -lint "$tmp"
  rm -f "$tmp"
  echo "plutil -lint OK"
fi

PREFIX=${CHATDEX_CI_PREFIX:-$HOME/tmp-install/chatdex-macos-ci}
rm -rf "$PREFIX"
mkdir -p "$PREFIX"

export CHATDEX_HOME="$PREFIX"
export CHATDEX_LABEL=dev.cygmris.chatdex.ci
export CHATDEX_BIN="$PREFIX/.local/bin/chatdex"
export CHATDEX_CONFIG="$PREFIX/.config/chatdex/config.json"
export CHATDEX_DB="$PREFIX/.local/share/chatdex/index.db"
export CHATDEX_PLIST="$PREFIX/Library/LaunchAgents/${CHATDEX_LABEL}.plist"
export CHATDEX_LOG_DIR="$PREFIX/Library/Logs/chatdex"

# 合成会话：只写在隔离 HOME 下，绝不碰 ~/.claude
proj="$PREFIX/.claude/projects/-ci-demo"
mkdir -p "$proj"
printf '%s\n' '{"type":"user","timestamp":"2026-09-06T00:00:00.000Z","cwd":"/tmp/ci","sessionId":"ci-1","message":{"role":"user","content":"macos ci health check"}}' >"$proj/a.jsonl"

mkdir -p "$(dirname "$CHATDEX_CONFIG")"
cat >"$CHATDEX_CONFIG" <<EOF
{
  "home": "$PREFIX",
  "db_path": "$CHATDEX_DB",
  "summary": {"enabled": false},
  "backup": {"after_scan": false},
  "scan": {"interval_sec": 5},
  "ports": {"ui": 15021, "api": 15022}
}
EOF
chmod 600 "$CHATDEX_CONFIG"

if [ -z "${CHATDEX_PREBUILT:-}" ]; then
  echo "== 本机构建 =="
  staging=$(mktemp -d)/chatdex
  (cd "$REPO_ROOT" && CGO_ENABLED=0 go build \
    -ldflags "-X github.com/cygmris/chatdex/internal/version.Version=ci-test -X github.com/cygmris/chatdex/internal/version.Commit=ci" \
    -o "$staging" ./cmd/chatdex)
  export CHATDEX_PREBUILT=$staging
fi

cleanup() { "$SERVICE" uninstall >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== install =="
"$SERVICE" install
"$SERVICE" status
curl -fsS http://127.0.0.1:15022/api/health | grep '"ok":true'
"$CHATDEX_BIN" version | grep .
i=0
while [ "$i" -lt 30 ]; do
  n=$(python3 -c "import json,urllib.request; print(json.load(urllib.request.urlopen('http://127.0.0.1:15022/api/health'))['index']['sessions'])")
  [ "$n" -ge 1 ] && break
  i=$((i + 1))
  sleep 1
done
[ "${n:-0}" -ge 1 ] || { echo "FAIL: 30s 内索引仍为空"; exit 1; }

echo "== 第二个实例不得写坏 index.db =="
before=$(wc -c <"$CHATDEX_DB")
if "$CHATDEX_BIN" serve --config "$CHATDEX_CONFIG"; then
  echo "FAIL: 第二个实例竟然启动成功"
  "$SERVICE" uninstall || true
  exit 1
fi
after=$(wc -c <"$CHATDEX_DB")
[ "$before" = "$after" ] || { echo "FAIL: 第二个实例动了索引库"; "$SERVICE" uninstall || true; exit 1; }
echo "第二个实例已拒绝，索引库未动"

echo "== 停服务后升级，索引保留 =="
"$SERVICE" stop
sessions_before=$(python3 -c "import sqlite3; print(sqlite3.connect('$CHATDEX_DB').execute('select count(*) from sessions').fetchone()[0])")
"$SERVICE" upgrade --prebuilt
"$SERVICE" status
sessions_after=$(python3 -c "import json,urllib.request; print(json.load(urllib.request.urlopen('http://127.0.0.1:15022/api/health'))['index']['sessions'])")
[ "$sessions_before" = "$sessions_after" ] || {
  echo "FAIL: 升级后会话数 $sessions_before → $sessions_after"
  "$SERVICE" uninstall || true
  exit 1
}
curl -fsS http://127.0.0.1:15022/api/health | grep '"ok":true'

echo "== uninstall 保留配置和索引 =="
"$SERVICE" uninstall
[ -f "$CHATDEX_CONFIG" ] || { echo "FAIL: 卸载删了 config.json"; exit 1; }
[ -f "$CHATDEX_DB" ] || { echo "FAIL: 卸载删了 index.db"; exit 1; }
[ -f "$proj/a.jsonl" ] || { echo "FAIL: 卸载动了会话源文件"; exit 1; }
[ ! -f "$CHATDEX_BIN" ] || { echo "FAIL: 卸载后二进制还在"; exit 1; }

trap - EXIT
echo "macos-ci OK"
