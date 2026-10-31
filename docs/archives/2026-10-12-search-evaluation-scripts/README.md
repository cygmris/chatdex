# 检索评估脚本原件归档

归档日期：2026-10-12。来源为 Claude 会话 `6d29203f-a043-4a86-a474-8a6fc4a7fc6c` 的 scratchpad：

`<会话 scratchpad>/`

清理 `/agentdata` 时，项目已有[词汇鸿沟评估报告](../../eval-vocabulary-gap-2026-09-15.md)，但在当前 Git 跟踪的 `scripts/`、`docs/` 普通小文件中未找到这七个脚本的同 SHA-256 副本。因此将原件按字节保全，供后续理解、复用实验方法；不是把历史实验直接提升为现行测试工具。

| 原文件 | 可复用部分 | 复用前必须核对 |
|---|---|---|
| `gap_build.py` | 摘要与正文词汇差异、文档频率与词性过滤、阳性对照构造 | 索引库路径、schema、采样偏差；依赖 `jieba` |
| `gap_corpus.py` | 固定两侧相同检索域、跨会话均匀采样 | `gapset.json` 来源及索引库路径 |
| `gap_embed.py` | 分块嵌入、断点续跑 | 本机 Ollama、`bge-m3`、`numpy` 及断点与语料的一致性 |
| `gap_sparse.py` | FTS5 稀疏检索和块级聚合 | 不能沿用原注释对当前 ChatDex 实现的等价性断言，须重新对照代码 |
| `gap_dense.py` | 分块向量转为会话排序 | `emb.npy`、`emb_meta.json` 和语料必须来自同一轮 |
| `tokprobe.py` | tokenizer 大小和构建时间对照 | 硬编码输出目录、原始会话范围；会替换自身输出目录中的同名实验数据库 |
| `perf.py` | 固定查询组的延迟采样 | 当前 API 返回结构；打印 `FAIL` 不等于返回非零退出码，不能直接作 CI 门禁 |

## 边界

- 七个 `.py` 是历史原件，保留原注释和路径，不因归档而宣称现在可以直接运行。SHA-256 见 `manifest.json`。
- 两个构建语料脚本仍指向旧的 `~/.local/share/chatdex/index.db`；不能将它们当作当前本机数据库配置。
- `gap_sparse.py` 提到的 `gap_eval.py` 不在本次归档中；其 `--with-summary` 分支读取 `summaries`，而原 `gap_corpus.py` 不负责写这个字段，不能假设七个文件直接串起来就能复现报告全部结果。
- `gap_embed.py` 和 `gap_dense.py` 的请求指向 `127.0.0.1:11434`；其固定 `Authorization` 值已核对为本地占位值，不是迁入的账号凭据。
- 不归档真实会话语料、查询结果、embedding、SQLite 数据库、截图、`r2env.sh` 或同步凭据。相邻 `.gitignore` 阻止误加实验输出。
- 本次只做原件哈希一致性和 Python 语法检查，没有执行查询、生成 embedding、复跑基准或重新认可旧性能结论。
- 临时目录原件仍保留，避免打断旧会话引用。后续若整理为正式工具，应在项目 `scripts/` 中参数化输入输出并补合成数据测试，而不是新建全域 skill。
