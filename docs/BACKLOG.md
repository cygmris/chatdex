# BACKLOG

状态机：`idea → roadmap → spec'd → shipped(R#)`。

## 必做

| id | 方向 | 优先级 | 状态 | 关联 |
|----|------|--------|------|------|
| R1 | 统一索引并检索 Claude Code 与 Codex 的全部会话记录：Go 常驻单例 + systemd --user + MCP 端点 + 只读 Web dashboard（形态照搬 specloop）。解决「找不到上次那段对话」——决策与结论大量沉淀在会话里却无检索入口。 | P1 | shipped(R1) | 参考实现 specloop（同作者） |
| R2 | dashboard 视觉与交互重做：走 claude-design 出设计稿（明暗双主题 design tokens + 五页组件规范）再实现，并补齐三项缺失能力——明暗切换、摘要浏览入口、设置页（可视化改 config 并生效）。设置页需修订「Web dashboard 为只读」这条 Usability 需求（只读的本意是不碰会话原始文件，该安全不变式不动）。 | P1 | roadmap | 用户反馈：UI 丑、只有暗色无切换、不知道去哪看摘要、没有设置界面；验收走 design-handoff-parity |

## Ideas

| id | 方向 | 优先级 | 状态 | 关联 |
|----|------|--------|------|------|
