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
    source            TEXT    NOT NULL,          -- claude | codex
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

CREATE TABLE IF NOT EXISTS blocks (
    id          INTEGER PRIMARY KEY,
    session_id  INTEGER NOT NULL REFERENCES sessions(id),
    seq         INTEGER NOT NULL,               -- 会话内序号，回读定位与命中跳转
    ts          INTEGER NOT NULL DEFAULT 0,
    kind        TEXT    NOT NULL,               -- user|assistant|tool_use|tool_result|summary
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
}
