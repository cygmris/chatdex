#!/bin/sh
# 从源码构建 chatdex 并安装为当前用户的 launchd LaunchAgent。
# 不使用 root / sudo。已有 config.json 与 index.db 不会被覆盖。
set -eu
SCRIPT_DIR=$(CDPATH= cd -- "$(dirname "$0")" && pwd)
exec "$SCRIPT_DIR/macos-service.sh" install --build "$@"
