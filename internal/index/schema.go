package index

// schemaSQL 建立索引库的全部结构。
//
// blocks_fts 用**外部内容表**（content='blocks'）：正文只存一份在 blocks.body，
// FTS5 只存倒排索引。这在 GB 量级下不是小事——contentless 表虽然更省，但提供不了
// snippet()，而命中高亮是需求 3.2 的硬要求。
//
// FTS 索引的维护交给触发器，Go 侧只操作 blocks 表，不手写 'delete' 指令，
// 少一类「忘了同步 FTS」的 bug。
//
// ⛔ 这里没有、也不得有任何 embedding / 向量列或表（需求 8 门控，本期不实现）。
const schemaSQL = `
CREATE TABLE IF NOT EXISTS sessions (
    id                INTEGER PRIMARY KEY,
    source            TEXT    NOT NULL,          -- claude | codex | grok
    session_uid       TEXT    NOT NULL,
    parent_uid        TEXT    NOT NULL DEFAULT '',
    agent_label       TEXT    NOT NULL DEFAULT '',
    file_path         TEXT    NOT NULL UNIQUE,   -- 原始文件绝对路径
    project_path      TEXT    NOT NULL DEFAULT '',
    started_at        INTEGER NOT NULL DEFAULT 0,
    ended_at          INTEGER NOT NULL DEFAULT 0,
    msg_count         INTEGER NOT NULL DEFAULT 0,
    summary           TEXT,                      -- NULL = 尚未生成，缺摘要不阻断检索
    summary_model     TEXT    NOT NULL DEFAULT '',
    summary_at        INTEGER NOT NULL DEFAULT 0,
    summary_msg_count INTEGER NOT NULL DEFAULT 0, -- 生成摘要时的消息数，用于判断会话是否已显著增长（R11.8）
    size              INTEGER NOT NULL DEFAULT 0, -- 增量水位
    mtime             INTEGER NOT NULL DEFAULT 0,
    offset            INTEGER NOT NULL DEFAULT 0,
    alive             INTEGER NOT NULL DEFAULT 1  -- 0 = 原始文件已消失
);
CREATE INDEX IF NOT EXISTS sessions_project ON sessions(project_path, started_at);
CREATE INDEX IF NOT EXISTS sessions_time    ON sessions(started_at);
-- 给「取某会话的子代理」的等值查询用。三态过滤（parent_uid != ''）**不需要**它：
-- 那个条件选择率接近 50%，走索引反而更慢，SQLite 会自己选全表扫。
CREATE INDEX IF NOT EXISTS sessions_parent  ON sessions(parent_uid);

CREATE TABLE IF NOT EXISTS blocks (
    id          INTEGER PRIMARY KEY,
    session_id  INTEGER NOT NULL REFERENCES sessions(id),
    seq         INTEGER NOT NULL,               -- 会话内序号，回读定位与命中跳转
    ts          INTEGER NOT NULL DEFAULT 0,
    kind        TEXT    NOT NULL,               -- user|assistant|reasoning|tool_use|tool_result|summary
    tool_name   TEXT    NOT NULL DEFAULT '',
    tool_use_id TEXT    NOT NULL DEFAULT '',
    truncated   INTEGER NOT NULL DEFAULT 0,
    raw_bytes   INTEGER NOT NULL DEFAULT 0,
    body        TEXT    NOT NULL               -- 已插入 U+0001 CJK 分隔符
);
CREATE INDEX IF NOT EXISTS blocks_session ON blocks(session_id, seq);

CREATE VIRTUAL TABLE IF NOT EXISTS blocks_fts USING fts5(
    body,
    content='blocks',
    content_rowid='id',
    tokenize="unicode61 remove_diacritics 2"
);

CREATE TRIGGER IF NOT EXISTS blocks_ai AFTER INSERT ON blocks BEGIN
    INSERT INTO blocks_fts(rowid, body) VALUES (new.id, new.body);
END;
CREATE TRIGGER IF NOT EXISTS blocks_ad AFTER DELETE ON blocks BEGIN
    INSERT INTO blocks_fts(blocks_fts, rowid, body) VALUES ('delete', old.id, old.body);
END;
CREATE TRIGGER IF NOT EXISTS blocks_au AFTER UPDATE ON blocks BEGIN
    INSERT INTO blocks_fts(blocks_fts, rowid, body) VALUES ('delete', old.id, old.body);
    INSERT INTO blocks_fts(rowid, body) VALUES (new.id, new.body);
END;

CREATE TABLE IF NOT EXISTS meta (
    k TEXT PRIMARY KEY,
    v TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS summary_queue (
    session_id INTEGER PRIMARY KEY REFERENCES sessions(id),
    priority   INTEGER NOT NULL DEFAULT 1,      -- 0 新会话优先 / 1 历史批量
    state      TEXT    NOT NULL DEFAULT 'pending', -- pending|running|done|failed
    attempts   INTEGER NOT NULL DEFAULT 0,
    err        TEXT    NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS summary_queue_pick ON summary_queue(state, priority, session_id);

-- backup_seen / backup_scanned：「这个文件曾经出现在任一快照里吗」的并集索引。
--
-- 存在的理由：覆盖率此前只跟**最新一个快照**比，而源文件已消失的会话按定义
-- 就不在最新快照里 —— 它只活在旧快照。于是「还救得回来」恒为 0、「永久丢失」
-- 恒等于全部消失的会话数。实测 1973 个「永久丢失」里随机 29 个样本有 25 个
-- （86%）在旧快照里找得到。
--
-- 为什么要缓存而不是每次现查：实测单个快照 restic ls 约 1 秒 / 7000 个文件，
-- 当前 566 个快照 —— 全扫 9.4 分钟，不可能放在请求路径上。
--
-- **这是派生数据**：整表删掉能重建，与「索引库 index.db 不进备份源」同一条纪律。
CREATE TABLE IF NOT EXISTS backup_seen (
    path TEXT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS backup_scanned (
    snapshot_id TEXT PRIMARY KEY,
    scanned_at  INTEGER NOT NULL
);
`

// migrations 是建表之后要补的增量结构变更。
//
// CREATE TABLE IF NOT EXISTS 只在库不存在时生效，对**已有**的库加不了列——
// 忘了这一步，新字段在开发机上（新建库）一切正常，一升级到已有索引就炸。
// 每条执行时忽略「列已存在」错误，于是重复运行是安全的。
var migrations = []string{
	`ALTER TABLE sessions ADD COLUMN summary_msg_count INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE sessions ADD COLUMN title TEXT NOT NULL DEFAULT ''`,
}

// repairs 是每次启动都跑一遍的数据自愈语句。
//
// 与 migrations 的区别：那是结构变更（跑一次就够），这是**修正存量数据**——
// 早期版本把 seq=-1 的摘要块算进了 msg_count，已受影响的会话不会自己改回来。
// 语句本身幂等，正常情况下命中 0 行，代价可以忽略。
var repairs = []string{
	`UPDATE sessions SET msg_count = (
	     SELECT COUNT(*) FROM blocks b WHERE b.session_id = sessions.id AND b.seq >= 0)
	 WHERE msg_count <> (
	     SELECT COUNT(*) FROM blocks b WHERE b.session_id = sessions.id AND b.seq >= 0)`,

	// Grok 的正文来源从 chat_history.jsonl 换成同目录的 updates.jsonl（R25）。
	// 前者会被 Grok CLI 原地压缩，索引它等于让内容随压缩静默消失；实测 1279 条
	// 提问里 chat_history 只剩 15%，另外 82% 只存在于 updates.jsonl。
	//
	// file_path 是 UNIQUE 键、水位挂在它上面，所以必须改写身份并把水位归零，
	// 让下一轮扫描从 updates.jsonl 整个重建。旧摘要是按压缩后那 15% 生成的，
	// 内容重建后不再成立，一并作废。
	//
	// 🔴 **顺序是「先改身份、再清残块」，不能反。** repairs 每条走独立的
	// db.Exec（见 store.go），**没有事务**。若先删块再改路径，一旦中间崩掉就留下
	// 「块没了、路径还是旧的」——而下次启动的 LIKE 仍然命中，会把已经空了的会话
	// 再删一遍，看不出异常，但那个会话此后永远指着一个会被压缩的文件。
	// 反过来则中间态自己可识别：offset=0 恰好就是「已改身份、块还没清」这个集合，
	// 下次启动②照样命中，自愈。**幂等性来自结构，不来自事务。**
	`UPDATE sessions
	    SET file_path = replace(file_path, '/chat_history.jsonl', '/updates.jsonl'),
	        size = 0, mtime = 0, "offset" = 0, msg_count = 0,
	        summary = NULL, summary_at = 0, summary_msg_count = 0
	  WHERE source = 'grok' AND file_path LIKE '%/chat_history.jsonl'`,

	// ② 清掉水位已归零却还留着块的 Grok 会话。FTS 由 blocks 的触发器同步，
	// 不必手写 'delete' 指令。正常情况下命中 0 行（新发现还没索引的会话本来就没块）。
	//
	// ⚠️ **判据只能是 "offset" = 0，不能再加 msg_count = 0**：上面那条 msg_count
	// 自愈语句**跑在本条之前**，会把中间态会话的 msg_count 从 0 重算回真实块数。
	// 于是「offset=0 AND msg_count=0」在崩溃恢复时恰好**不**命中——那正是它本该
	// 命中的唯一场景，残块从此永远清不掉。（变异扫描抓到：去掉 msg_count 谓词
	// 没有任何测试变红，查下去才发现是这条谓词本身错了。）
	`DELETE FROM blocks WHERE session_id IN (
	     SELECT id FROM sessions WHERE source = 'grok' AND "offset" = 0)`,
}
