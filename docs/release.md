# 发版

维护者视角。用户怎么装见 [deploy.md](deploy.md)。

## 版本号

不跟提交数走，按「用户要不要重做点什么」判：

| 情况 | 升哪位 |
|---|---|
| 要重建索引 / 配置项改名 / 端口变 / MCP 工具签名变 | MINOR |
| 加功能、加来源，旧的照常用 | MINOR |
| 只修 bug、文档、界面细节 | PATCH |
| 接口稳定、破坏性变更会提前告知 | 留给 1.0.0 |

## 步骤（v0.2.0 实跑过，2026-11-01）

1. **公开仓同步到最新**：`CHATDEX_PUBLIC_DIR=<公开仓> scripts/sync-public.sh -m "<信息>"`。
   它会拦截图过期（`docs/images/manifest.json` 的 `ui_digest` 落后于 `scripts/ui-digest.sh`）与黑名单命中；
   界面改过就先 `node scripts/screenshots.mjs --home <演示 HOME>` 重拍。
2. **先改文档里的版本号再同步**：README / README.en 的安装片段、deploy.md 的 ldflags 示例，
   否则 tag 指向的提交里还写着上一版的包名。
3. **在公开仓工作树交叉编译**（纯 Go、`CGO_ENABLED=0`，四个平台一台机器出齐；CI 只校验能编译，不产资产）：

   ```bash
   cd <公开仓> && C=$(git rev-parse --short HEAD) && V=0.2.0
   for t in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
     os=${t%/*}; arch=${t#*/}; d=out/chatdex_${V}_${os}_${arch}; mkdir -p "$d"
     CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
       -ldflags "-s -w -X github.com/cygmris/chatdex/internal/version.Version=$V -X github.com/cygmris/chatdex/internal/version.Commit=$C" \
       -o "$d/chatdex" ./cmd/chatdex
     cp README.md README.en.md LICENSE "$d/" && (cd out && tar -czf chatdex_${V}_${os}_${arch}.tar.gz chatdex_${V}_${os}_${arch})
   done
   (cd out && sha256sum *.tar.gz > SHA256SUMS)
   out/chatdex_${V}_linux_amd64/chatdex version     # 应打印 <V> (<C>)
   ```

   包的布局固定为 `chatdex_<ver>_<os>_<arch>/{chatdex,README.md,README.en.md,LICENSE}`，README 的安装片段按这个写。
4. **打 tag、发 release**：

   ```bash
   git tag -a v$V -m "v$V" && git push github v$V
   gh release create v$V --verify-tag --title "v$V —— <一句话>" --notes-file <说明> out/chatdex_${V}_*.tar.gz out/SHA256SUMS
   ```

   说明的结构照上一版：装 / 从上一版升级（停服务再换二进制；索引库启动时自动补列）/ 接 MCP / 这个版本有什么 / 安全边界 / 已知边界。
5. **私有仓打同名 tag** 指向对应提交（tag 说明写公开仓短 sha），推到两个远端。两边历史不同，tag 是唯一的对应关系。
6. **核对**：`gh release view v$V --json assets`，下载 `SHA256SUMS` 与本地 `diff`；README 顶部的 Release 徽章是动态的，几分钟后会变。

## 作者

两个仓都设了 local `user.name`，提交与 tag 都应是同一个名字；发布脚本在未设时拒绝提交。
怀疑「混进别的作者」时查三处：`git log --format='%an <%ae>' | sort | uniq -c`、`%cn` 同上、
`gh api repos/<owner>/<repo>/commits --paginate -q '.[].author.login'`。2026-11-01 查过一次，三处都只有一个名字。
