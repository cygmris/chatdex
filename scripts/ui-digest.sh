#!/usr/bin/env bash
# 界面摘要：对 git 跟踪的前端静态资源与配置元信息做 sha256，取前 16 位。
# 只看文件内容不看历史——私有仓与公开仓（历史重写过）算出来相同。
# 拍截图时写进 docs/images/manifest.json；发布脚本拿它判「截图落后于界面」。
# internal/config 也算进来：设置页是由它的元信息生成的。
# 盲区：后端其它页面文案（备份页、进度页的句子在 .go 里）改了不会变摘要。
# 测试文件不算：它们不会被嵌进界面，改一个夹具名不该让截图「过期」。
set -euo pipefail
cd "$(dirname "$0")/.."
git ls-files -z -- internal/dashboard/static internal/config \
  | grep -z -vE '_test\.(mjs|go)$' | sort -z | xargs -0 sha256sum | sha256sum | cut -c1-16
