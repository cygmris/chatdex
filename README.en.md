# chatdex

**Make every past Claude Code and Codex session searchable.** Runs locally, reads only, never phones home.

[简体中文](README.md) | English

[![Go](https://img.shields.io/badge/Go-1.26-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![SQLite FTS5](https://img.shields.io/badge/SQLite-FTS5-003B57?logo=sqlite&logoColor=white)](https://sqlite.org/fts5.html)
[![MCP](https://img.shields.io/badge/MCP-endpoint-6E56CF)](https://modelcontextprotocol.io)

> [!NOTE]
> **The dashboard UI is currently Chinese-only.** The code, docs, and this README are in English, but
> the interface is not localized yet — navigation reads 检索 / 时间线 / 摘要 / 问一问 / 设置
> (Search / Timeline / Summaries / Ask / Settings). Search itself is language-agnostic and works fine
> on English transcripts. If you'd use an English UI, say so in an issue and it moves up the list.

> Every decision you made with an AI assistant, every trap you fell into, every command you finally
> got right — it's all sitting in thousands of JSONL files with no way back in. chatdex gives you
> that way in. And gives it to your agents too.

![Search](docs/images/search.png)

---

## Why

`~/.claude/projects/` and `~/.codex/sessions/` hold your entire working history. `grep` doesn't cut it:

- **CJK doesn't match.** SQLite FTS5's `unicode61` treats a whole Chinese sentence as one token, so
  searching 「限流」 never finds 「请求限流」.
- **Most hits ≠ what you want.** Measured on a real corpus: the two sessions with the most hits
  (2272 and 2154) were both wrong; the one I wanted had 669. Those two just mentioned the word
  repeatedly in build logs.
- **The answer is usually in a tool call.** "How did I write that command last time" isn't in the
  prose — it's in the `tool_use` arguments.
- **Your words aren't the transcript's words.** You remember "incremental backup"; the transcript
  says "something like TimeMachine".

chatdex has a specific answer to each, with measured numbers behind it — see
[`docs/architecture.md`](docs/architecture.md).

## Features

| | |
|---|---|
| 🔍 **Mixed CJK/ASCII full-text search** | Per-character CJK splitting over FTS5. Median latency **37 ms** on a real 3 265-session / 747 K-block corpus |
| 🧠 **Summaries are indexed too** | A local LLM writes one line per session, **rephrasing in conceptual terms** — which is what closes the vocabulary gap above |
| 💬 **Ask** | Ask in plain language; the LLM rewrites its query and retries across rounds, and **shows you every query it tried**; scope it to a single project or ask across everything |
| 🏷 **Session names** | A name you set with `/rename` takes precedence over the LLM summary — what you called it beats what a model guessed |
| 🕘 **Timeline & transcript replay** | Grouped by project; click through to read the original exchange, paginated for long sessions |
| 🧬 **Subagents linked up** | Nearly half the sessions are subagents (48.5% on this machine). Filter to main sessions only or subagents only; expand a main session to see the subagents it dispatched, and jump back from a subagent to its parent |
| 📝 **Markdown, ANSI & syntax highlighting** | Assistant output renders as Markdown; ANSI colours in command output are coloured; code and commands are syntax-highlighted (colour scheme selectable, the default follows the interface theme); mermaid diagrams render on click. One click switches the transcript back to **raw bytes** |
| 🔗 **Shareable links** | View, query, every filter, and the session you're reading all live in the URL — send it to someone and they get the same result. The back button works too |
| 🔌 **MCP endpoint** | Your agent can look up "how did I solve this last time" by itself |
| 🎨 **Four themes** | Light/dark/follow-system, all contrast ratios verified against WCAG AA by script |
| ⚙️ **Settings UI** | Change config in the browser; most options take effect immediately |
| 📈 **Generation progress** | Where summarising is up to, how long is left, which sessions failed and why, one-click retry; and a **time window** so it only runs when you want (e.g. `02:00-08:00` overnight, wrapping past midnight is fine) |
| 🗄 **Backups (via restic)** | restic keeps it safe; chatdex answers what restic cannot — **are the sessions you indexed actually in the backup?** — and reads an original back once its source file is gone. restic is optional |
| 🔒 **Read-only, localhost-only** | See [Security boundaries](#security-boundaries) |

## Quick start

Requires Go 1.26+. **No** Node, no build chain, no Docker, no network access.

```bash
git clone https://github.com/cygmris/chatdex.git && cd chatdex
go build -o ~/.local/bin/chatdex ./cmd/chatdex

chatdex index      # first full index — ~13 min for 3 000 sessions
chatdex serve      # dashboard :5021 / API+MCP :5022
```

Open <http://127.0.0.1:5021>. To keep it running:

```bash
cp deploy/systemd/chatdex.service ~/.config/systemd/user/
systemctl --user enable --now chatdex
```

Full deployment and troubleshooting: [`docs/deploy.md`](docs/deploy.md).

### Optional: local LLM

Summaries and Ask need a local [Ollama](https://ollama.com). **It is an optional dependency** —
without it indexing and search work exactly the same; you just lose the Ask tab and the summary line.

```bash
ollama pull qwen2.5:7b-instruct
```

The endpoint **only accepts loopback addresses**. A remote address is rejected outright and there is
no flag to relax it — see below for why.

### Wire up MCP

Let an agent search its own history:

```json
{
  "mcpServers": {
    "chatdex": { "url": "http://127.0.0.1:5022/mcp" }
  }
}
```

Three tools: `search_sessions`, `get_session`, `list_projects`.

> ⚠️ **Streamable HTTP, not stdio.** chatdex is a long-running service and the MCP
> endpoint lives on the process that is already running (`:5022/mcp`), so the config
> takes a `url`. Do **not** write it as `{"command": "chatdex", "args": ["serve"]}` —
> that stdio form makes the client wait for JSON-RPC on stdin while the process is
> listening on HTTP, and neither side ever hears from the other.
> Start `chatdex serve` (or run it under systemd), then point the client at the URL above.

## Screenshots

> All screenshots use **synthetic demo data** — 57 fabricated sessions across 5 fictional projects.
> The generator is in the repo: [`scripts/gen-demo-corpus.py`](scripts/gen-demo-corpus.py), so you can verify that claim.
> Not anyone's real transcripts.

### Search: summary as the headline, snippet as evidence

Every result leads with the session summary, so you can tell at a glance which one you want.
The snippet below it shows *why* it matched.

![Search](docs/images/search.png)

### Filter down to the tool call

Seven filters: source, **main session / subagent**, content kind, tool name, project, and date range.
Filtering to tool calls is how you answer "how did I write that command last time" — the match lands
right on the command itself.

![Filtered search](docs/images/search-filters.png)

### Subagents linked up

Nearly half the sessions are subagents (1591 of 3265 on this machine). They used to sit in the results
with no way to tell them apart or filter them out. Now you can view main sessions only or subagents
only; a main session expands to the subagents it dispatched, and a subagent links back to its parent.

![Subagents](docs/images/subagents.png)

### Summaries

Browse or search all session summaries. Each shows which model generated it and when, because a
summary's trustworthiness depends on that.

![Summaries](docs/images/digest.png)

### Ask

Ask in plain language. The LLM rewrites the query and retries — **and every round is shown**, so you
can tell whether it searched in a sensible direction. Session IDs in the answer are clickable.

![Ask](docs/images/chat.png)

### Timeline

Grouped by project, newest first — useful for "what was I even doing that week".

![Timeline](docs/images/timeline.png)

### Transcript replay

Read the original exchange message by message. Assistant output renders as Markdown, ANSI colours
in command output are coloured, code blocks and commands are **syntax-highlighted** (highlight.js is
bundled; pick a scheme in Settings, or keep the default that follows the interface theme). The
**output** of file-reading commands (`cat`, `sed -n`, …) is highlighted too — the language comes from
the filename in the command, and when it can't be determined the output is left alone; output is
**never** auto-detected, which would just paint build logs at random. Mermaid
diagrams show their source until you click *Render*, and **tool calls render structurally** — a command looks like a command
(copy it and it runs), a file edit shows before/after, a patch shows as a coloured diff. One click switches to **raw** —
this is a forensic tool, and sometimes the exact stored bytes are the point. The session id lives in
the URL, so reloading or sharing lands in the same place.

![Transcript replay](docs/images/reader.png)

Syntax highlighting and on-click mermaid rendering (the renderer is 3.4 MB and is not fetched unless
you ask for a diagram):

![Syntax highlighting and diagrams](docs/images/reader-highlight.png)

The **output** of a file-reading command is highlighted as source, while the `go test` output right
below it stays plain — the language is inferred from the command only, and nothing is painted when it
can't be:

![Highlighted command output](docs/images/output-highlight.png)

### Generation progress

Summaries are generated in the background. This page says where it is, how long is left, and
**which sessions failed and why** — retry them one by one or all at once. You can also confine
generation to a time window, so it runs overnight instead of competing for your GPU during the day.

![Generation progress](docs/images/progress.png)

### Backups

restic sees paths; it has no idea what a session is. This page answers the question restic cannot —
it checks every session in the index against the latest snapshot and splits the result four ways:
**covered / not backed up (source still there) / permanently lost / source gone but in the backup**.
The middle two are both "not backed up", but one is fixed by ticking a directory in Settings and the
other is gone forever — collapsing them into one number would be a lie.

![Backups](docs/images/backup.png)

Sessions in the last group can be read **straight from the backup** in the transcript view — and that
copy is *more complete* than the index: tool results are deliberately truncated when indexed
(4096 bytes by default), so the full text only exists in the original. The UI shows a
`restic restore` command you can copy, but never runs it for you.

![Reading the original from the backup](docs/images/archived.png)

chatdex is **not a wrapper around restic**: no scheduling, no retention policy, and it never runs a
restore for you (the UI just shows a command you can copy). Without restic installed, indexing and
search are unaffected — the backup entry points are greyed out with the reason shown.

### Settings

Every config option, rendered from a single declaration in the backend. Options needing a restart say
so; index options note that they only affect newly indexed content.

![Settings](docs/images/settings.png)

### Collapsible sidebar

Collapses to 46 px with single-character navigation, for when you want the width back.

![Collapsed sidebar](docs/images/sidebar-mini.png)

## Security boundaries

Your transcripts contain configs you `cat`-ed, variables you `env`-ed, tokens you passed to `curl`.
**The index is a concentrated copy of all that.** So these four are hard constraints with no opt-out,
each covered by tests:

| Constraint | How it's enforced |
|---|---|
| **Never writes session files** | Always `os.Open` (`O_RDONLY`). An E2E test byte-compares size / mtime / content of the originals after a full run |
| **Binds `127.0.0.1` only** | The address is not a config option. An integration test actually dials the LAN IP and fails if it connects |
| **Index DB is `0600`** | Main DB plus `-wal` / `-shm` all explicitly chmod-ed; directory `0700` |
| **LLM endpoint must be loopback** | Construction fails outright — no `--allow-remote` escape hatch. Seven negative tests covering remote, LAN, and wildcard addresses |

The config file is `0600` too, written via `.tmp → chmod → rename` so a power cut can't leave half a
JSON behind.

## The index is **not** a backup (that is restic's job)

The index stores *derived* text, not a copy of the original. Measured: 5.9 GB of source transcripts
became 549 MB of indexed text (~9%). The gap is JSONL structural overhead plus these deliberate losses:

- Tool results are **truncated at 4096 bytes** (configurable) — 38 K of 650 K blocks were truncated
- Images, binaries, and reasoning traces are **not indexed**
- The `CLAUDE.md` / `AGENTS.md` text injected into every session's first message is **stripped**

The original `.jsonl` files remain the only source of truth; every record stores their absolute path
and offset so it can point back.

**Backups are restic's job; chatdex only does the part restic cannot.** The split: restic handles
*keeping it safe* — content-addressed dedup, compression, encryption, `restic check`. chatdex handles
what restic has no way of knowing — which paths matter, **whether the sessions you indexed are
actually in the backup**, and how to read an original back once its source file is gone. restic sees
paths; it has no idea what a session is.

chatdex is **not a wrapper around restic**: no scheduling (that is what a systemd timer is for),
no retention policy, and it never performs a restore for you (the read-only rule applies to recovery
too — the UI shows a command you can copy). restic is an **optional dependency**: without it,
indexing and search work exactly as before and the backup entry points explain why they are greyed out.

## Measured

Real corpus on real hardware, not a synthetic benchmark:

All from one measurement run on 2026-08-05 (historical comparison in [architecture.md](docs/architecture.md)):

| | |
|---|---|
| Sessions / blocks | 3 265 (3 082 alive, 183 whose source file is gone) / 747 153 |
| Indexed text / index size | 0.59 GB / 3.1 GB |
| Search latency | median **37 ms**, p95 113 ms |
| Slowest query | 529 ms — the single CJK character 的, matching 121 K blocks |
| Summary throughput | median 0.8 s/session; full run took **2 h 13 min** (measured 2026-07-29, not re-run) |

## The two JSONL formats differ (read before writing a parser)

| | Claude Code | Codex |
|---|---|---|
| Path | `~/.claude/projects/<slug>/<uuid>.jsonl` | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` |
| Role lives at | `message.role` | `payload.role` (outer `type: response_item`) |
| Text field | `content` as string, or list items with `type=="text"` | list items with `type=="input_text"` |
| Subagents | separate `<uuid>/subagents/agent-*.jsonl` | same file |

Parsers are pluggable — implement the `Parser` interface in `internal/parser` and neither the index
nor the search layer needs to change.

## Why there's no vector search

The vocabulary gap is real: you remember "incremental backup", the transcript says "something like
TimeMachine", and keyword search returns nothing. But vectors aren't the only fix, or necessarily the
best one:

- **Summaries are text, and they rephrase in conceptual terms.** With the summary "discussed building
  an incremental backup tool on restic", searching "incremental backup" **hits via plain keywords**.
- **The agent rewrites its query and retries.** Vectors can't: they give one similarity ranking and
  never rephrase because the results looked wrong.
- In-process embeddings — model choice, binary size, full-corpus vectorization time, hybrid ranking
  tuning — are the single most expensive piece of this project.

So it's **gated**: collect 10 real cases where summaries *and* agent rewriting both failed. If this
design solves ≥8 of 10, the requirement is closed permanently; otherwise it reopens and that set
becomes its acceptance criteria. There is no embedding table or column pre-wired in the code.

## Docs

- [`docs/architecture.md`](docs/architecture.md) — architecture and nine key decisions **with their
  costs**, including the full post-mortem of a 63.8 s → 276 ms query fix
- [`docs/deploy.md`](docs/deploy.md) — deployment, configuration, troubleshooting
- [`docs/design-parity.md`](docs/design-parity.md) — where the UI departs from its design mock, and why

## License

MIT — see [`LICENSE`](LICENSE).

Bundled fonts are [IBM Plex](https://github.com/IBM/plex) (Sans / Mono) under the SIL Open Font
License 1.1; full text at `internal/dashboard/static/fonts/LICENSE.txt`. Bundling them is deliberate:
the page references no external domain, so it works offline and leaks nothing about your browsing to
a third party.

Rendering uses four libraries, also bundled (and likewise referencing no external domain):
[marked](https://github.com/markedjs/marked) (MIT),
[DOMPurify](https://github.com/cure53/DOMPurify) (Apache-2.0 / MPL-2.0),
[highlight.js](https://github.com/highlightjs/highlight.js) (BSD-3-Clause) and
[mermaid](https://github.com/mermaid-js/mermaid) (MIT).
mermaid weighs 3.4 MB and is **never part of the initial load** — it is fetched only when you click
*Render* on a diagram.
Full license texts live in `internal/dashboard/static/vendor/`.
