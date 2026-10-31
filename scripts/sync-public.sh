#!/usr/bin/env bash
# scripts/sync-public.sh — 把本仓 HEAD 整树覆盖到公开仓、压成一个提交并推送。每一步都是关卡，拦下就停。
#
# 用法：
#   CHATDEX_PUBLIC_DIR=<公开仓工作树> scripts/sync-public.sh -m "<提交信息>" [--allow-stale]
#
# 环境：
#   CHATDEX_PUBLIC_DIR     公开仓路径（必填，不写默认值——这个文件也会被同步过去）
#   CHATDEX_SANITIZE_LIST  黑名单文件，默认 ~/.config/chatdex-sanitize/patterns.txt；与公开仓的 pre-commit 共用，
#                          放在仓库外是有意的：把要隐藏的东西列进公开仓等于发布它
#
# 关卡顺序：
#   1 本仓工作树干净            2 截图不落后于界面（manifest.ui_digest == scripts/ui-digest.sh）
#   3 整树覆盖 + git add -A      4 公开仓 go build / vet / test -count=1
#   5 黑名单扫描（退出码三分：0 命中→停、1 未命中→继续、其它→扫描坏了→停）
#   6 公开仓已设 local user.name  7 commit（pre-commit 照常跑，不 --no-verify）→ push github → 打印作者
set -euo pipefail

die() { echo "✖ $*" >&2; exit 1; }
msg=''; allow_stale=0
while [ $# -gt 0 ]; do
  case "$1" in
    -m) msg=${2:-}; shift 2 ;;
    --allow-stale) allow_stale=1; shift ;;
    *) die "不认识的参数 $1" ;;
  esac
done
[ -n "$msg" ] || die '缺 -m "<提交信息>"'

PRIV=$(cd "$(dirname "$0")/.." && pwd)
PUB=${CHATDEX_PUBLIC_DIR:?需要 CHATDEX_PUBLIC_DIR=<公开仓工作树>}
PUB=$(cd "$PUB" && pwd)
LIST=${CHATDEX_SANITIZE_LIST:-$HOME/.config/chatdex-sanitize/patterns.txt}
[ "$PUB" != "$PRIV" ] || die "CHATDEX_PUBLIC_DIR 指向了本仓自己"
[ -d "$PUB/.git" ] || die "$PUB 不是 git 仓"
git -C "$PUB" remote | grep -qx github || die "$PUB 没有 github 远端，不像公开仓"

echo "1/7 本仓工作树"
[ -z "$(git -C "$PRIV" status --porcelain)" ] || die "本仓工作树不干净，先提交或清掉"

echo "2/7 截图是否落后于界面"
want=$(bash "$PRIV/scripts/ui-digest.sh")
have=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["ui_digest"])' "$PRIV/docs/images/manifest.json")
if [ "$want" != "$have" ]; then
  if [ "$allow_stale" = 1 ]; then
    echo "  ⚠ 截图落后于界面（manifest=$have 当前=$want），--allow-stale 放行"
  else
    die "截图落后于界面：manifest=$have 当前=$want。先 node scripts/screenshots.mjs 重拍，或明确 --allow-stale"
  fi
fi

echo "3/7 整树覆盖 $PUB"
tar=$(mktemp)
trap 'rm -f "$tar"' EXIT
git -C "$PRIV" archive --format=tar HEAD > "$tar"
(
  cd "$PUB"
  git ls-files -z | xargs -0 rm -f            # 先删旧的追踪文件，否则本仓已删的会残留
  find . -type d -empty -not -path './.git/*' -delete
  tar -xf "$tar"
  git add -A
)

echo "4/7 公开仓 build / vet / test"
( cd "$PUB" && go build ./... && go vet ./... && go test ./... -count=1 )

echo "5/7 黑名单扫描"
[ -f "$LIST" ] || die "黑名单文件不存在：$LIST（CHATDEX_SANITIZE_LIST 可指定）"
pats=$(mktemp)
grep -vE '^[[:space:]]*(#|$)' "$LIST" > "$pats" || die "黑名单剥掉注释后为空"
set +e
hits=$(cd "$PUB" && git grep -I -n -P -f "$pats" -- .)
rc=$?
set -e
rm -f "$pats"
case $rc in
  0) echo "$hits" | cut -d: -f1,2 >&2; die "黑名单命中（上面是 文件:行号），改掉再来" ;;
  1) ;;
  *) die "git grep 退出 $rc：扫描本身坏了，不能当作干净" ;;
esac

echo "6/7 作者"
author=$(git -C "$PUB" config --local user.name || true)
[ -n "$author" ] || die "公开仓没设 local user.name（全局默认不一定是对外用的那个名字）"

echo "7/7 提交并推送（作者 $author）"
if git -C "$PUB" diff --cached --quiet; then die "公开仓与本仓 HEAD 没有差异，没什么可提交"; fi
git -C "$PUB" commit -q -m "$msg"
git -C "$PUB" push github HEAD
git -C "$PUB" log -1 --format='✔ %an <%ae> %h %s'
