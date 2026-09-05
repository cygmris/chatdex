# chatdex

**Make every past Claude Code, Codex, and Grok CLI session searchable.** Runs locally, reads only, never phones home.

[简体中文](README.md) | English

[![CI](https://github.com/cygmris/chatdex/actions/workflows/ci.yml/badge.svg)](https://github.com/cygmris/chatdex/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/cygmris/chatdex)](https://github.com/cygmris/chatdex/releases/latest)
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

`~/.claude/projects/`, `~/.codex/sessions/`, and `~/.grok/sessions/` hold your entire working history. `grep` doesn't cut it:

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
| 🔍 **Mixed CJK/ASCII full-text search** | Per-character CJK splitting over FTS5. Median **53 ms** on a real corpus of 4 425 sessions / 1.33 M blocks |
| 🧠 **Summaries are indexed too** | A local LLM writes one line per session, **rephrasing in conceptual terms** — which is what closes the vocabulary gap above |
| 💬 **Ask** | Ask in plain language; the LLM rewrites its query and retries across rounds, and **shows you every query it tried**; scope it to a single project or ask across everything |
| 🏷 **Session names** | A name you set with `/rename` takes precedence over the LLM summary — what you called it beats what a model guessed |
| 🕘 **Timeline & transcript replay** | Grouped by project, paginated by project; filters apply here too; click through to read the original exchange, paginated for long sessions; **click “truncated” to open the original** (from disk if the file is still there, from the backup otherwise) |
| 🧬 **Subagents linked up** | Nearly half the sessions are subagents (2 082 of 4 425 on the author's machine). Filter to main sessions only or subagents only; expand a main session to see the subagents it dispatched, and jump back from a subagent to its parent |
| 📝 **Markdown, ANSI & syntax highlighting** | Assistant output renders as Markdown; ANSI colours in command output are coloured; code and commands are syntax-highlighted (colour scheme selectable, the default follows the interface theme); mermaid diagrams render on click. One click switches the transcript back to **raw bytes** |
| 🔗 **Shareable links** | View, query, every filter, and the session you're reading all live in the URL — send it to someone and they get the same result. The back button works too |
| 🔌 **MCP endpoint** | Your agent can look up "how did I solve this last time" by itself |
| 🎨 **Four themes** | Light/dark/follow-system, all contrast ratios verified against WCAG AA by script |
| ⚙️ **Settings UI** | Change config in the browser; most options take effect immediately |
| 📈 **Generation progress** | Where summarising is up to, how long is left, which sessions failed and why, one-click retry; and a **time window** so it only runs when you want (e.g. `02:00-08:00` overnight, wrapping past midnight is fine) |
| 🗄 **Backups (via restic)** | restic keeps it safe; chatdex answers what restic cannot — **are the sessions you indexed actually in the backup?** — and reads an original back once its source file is gone; the repo can also be mirrored to S3 / R2, reporting how many snapshots behind that copy is. restic is optional |
| 🔒 **Read-only, localhost-only** | See [Security boundaries](#security-boundaries) |

## Quick start

A single static binary. **No** Node, no build chain, no Docker, no network access.

Grab the archive for your platform from
[Releases](https://github.com/cygmris/chatdex/releases/latest)
(linux / macOS × amd64 / arm64):

```bash
tar -xzf chatdex_0.1.0_linux_amd64.tar.gz
install -m755 chatdex_0.1.0_linux_amd64/chatdex ~/.local/bin/chatdex

chatdex index      # first full index — ~13 min for 3 000 sessions
chatdex serve      # dashboard :5021 / API+MCP :5022
```

Or build it yourself (needs Go 1.26+):

```bash
git clone https://github.com/cygmris/chatdex.git && cd chatdex
go build -o ~/.local/bin/chatdex ./cmd/chatdex
```

Open <http://127.0.0.1:5021>. To keep it running:

Linux:

```bash
cp deploy/systemd/chatdex.service ~/.config/systemd/user/
systemctl --user enable --now chatdex
```

macOS (per-user launchd, no root):

```bash
./scripts/macos-install.sh
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

Nearly half the sessions are subagents (2 082 of 4 425). Left alone they sit in the results
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

restic sees paths; it has no idea what a session is. This page answers the question restic cannot.
It checks every session in the index, with a different test depending on the session: if the source
file **is still there**, it looks for it in the *latest* snapshot, because "am I protected right now"
only counts the newest one; if the source file **is already gone**, it looks in *any* snapshot,
because one surviving copy is enough to read it back. That yields four numbers:
**covered / not backed up (source still there) / permanently lost / source gone but in the backup**.
The middle two are both "not backed up", but one is fixed by ticking a directory in Settings and the
other is gone forever — collapsing them into one number would be a lie.

![Backups](docs/images/backup.png)

Sessions in the last group can be read **straight from the backup** in the transcript view — and that
copy is *more complete* than the index: tool results are deliberately truncated when indexed
(4096 bytes by default), so the full text only exists in the original. The UI shows a
`restic restore` command you can copy, but never runs it for you.

![Reading the original from the backup](docs/images/archived.png)

**It also tells you what you are still missing.** restic sees paths; it has no idea what
`~/.codex/memories` is. chatdex knows the layout of an agent's home, checks it against a known list,
and shows what on this machine *should* be backed up but isn't — global instructions, your own
skills / subagents / hooks, and Codex's memories (a git repo, but usually with local commits only and
no remote, so losing it means losing it). One line explaining each, one click to add it as a source.
**Paths that hold plaintext credentials are flagged but never pre-selected** — whether your API keys
go into the backup is your call.

**The coverage check says which snapshot it was computed against.** That is not decoration: the
"source still there" half always compares against the *latest* snapshot, and "latest" might be a week old — sessions created
since then count as neither covered nor uncovered, because they are not in the baseline at all, while
the page looks perfectly healthy. Anything older than a day raises an explicit warning.

**Offsite copy.** The restic repo can be mirrored to S3 / R2, and chatdex tells you **how many
snapshots behind that copy is** — which is the part that matters: a copy of unknown freshness cannot
be counted on when you are deciding whether you are safe. Configured under `backup.mirror`;
credentials live in a separate env file (**never in `config.json`**, which is served to the browser
and goes into the backup). Files are copied `config → keys → data → index → snapshots`, snapshots
strictly last, then verified file by file — **an exit code of 0 does not mean the files match**.
Automatic sync is **off** by default: it sends your data outbound, needs credentials, and uses
bandwidth, and none of those three should be decided for you by a default. Leaving it off does not
leave you in the dark — the page keeps reporting how far behind the copy is.


### Settings

Change any option in the browser. The ones that need a restart say so, and index options note that
they only affect content indexed from then on. (The page is generated from the backend's own config
metadata, so "added an option but forgot the settings page" cannot happen.)

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

The index stores *derived* text, not a copy of the original. Measured: 9.8 GB of source transcripts
became 1.18 GB of indexed text (~12%). The gap is JSONL structural overhead plus these deliberate losses:

- Tool results are **truncated at 4096 bytes** (configurable) — 95 K of 1.33 M blocks were truncated
- Images, binaries, and reasoning traces are **not indexed**
- The `CLAUDE.md` / `AGENTS.md` text injected into every session's first message is **stripped**

The original `.jsonl` files remain the only source of truth; every record stores their absolute path
and offset so it can point back.

**Backups are restic's job; chatdex only does the part restic cannot.** The split: restic handles
*keeping it safe* — content-addressed dedup, compression, encryption, `restic check`. chatdex handles
what restic has no way of knowing — which paths matter, **whether the sessions you indexed are
actually in the backup**, and how to read an original back once its source file is gone.

chatdex is **not a wrapper around restic**: no scheduling (that is what a systemd timer is for),
no retention policy, and it never performs a restore for you (the read-only rule applies to recovery
too — the UI shows a command you can copy). restic is an **optional dependency**: without it,
indexing and search work exactly as before and the backup entry points explain why they are greyed out.

## Measured

Real corpus on real hardware, not a synthetic benchmark:

Measured 2026-09-01 on the author's machine (historical comparison in [architecture.md](docs/architecture.md)):

| | |
|---|---|
| Sessions / blocks | 4 425 (2 336 alive, 2 089 whose source file is gone) / 1 331 761 |
| Per source | Claude Code 3 893 · Codex 439 · Grok CLI 93 |
| Indexed text / index size | 1.18 GB / 4.4 GB |
| Search latency | median **53 ms**, p95 124 ms (30 queries × 3 runs) |
| Slowest query | 958 ms — the single CJK character 的, matching hundreds of thousands of blocks |
| Summary throughput | median 0.8 s/session; full run took **2 h 13 min** (measured 2026-07-29, not re-run) |

## The three JSONL formats all differ (read before writing a parser)

| | Claude Code | Codex | Grok CLI |
|---|---|---|---|
| Path | `~/.claude/projects/<slug>/<uuid>.jsonl` | `~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl` | `~/.grok/sessions/<percent-encoded cwd>/<uuid>/chat_history.jsonl` |
| Role lives at | `message.role` | `payload.role` (outer `type: response_item`) | top-level `type` |
| Text field | `content` as string, or list items with `type=="text"` | list items with `type=="input_text"` | `content`; **reasoning is in `summary[].text`** |
| Timestamps | on every record | on every record | **absent from the transcript** — taken from `events.jsonl` next to it by `tool_call_id`, the rest interpolated |
| Metadata | scattered through the transcript | first line, `session_meta` | `summary.json` next to it |
| Subagents | separate `<uuid>/subagents/agent-*.jsonl` | same file | transcript sits at the project's top level; the parent's `subagents/<uuid>/` holds only `meta.json` |

> [!WARNING]
> For Grok, whether a session is a subagent **cannot be read off `agent_name` in `summary.json`**.
> On the corpus at hand it lines up perfectly with main/sub (41/41, 30/30) — but that is a
> coincidence of this corpus: the field means "which agent config was running". Use the structural
> fact instead (decision 40 in architecture.md).

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

So it is not being built yet, but the bar is written down: collect 10 real cases where summaries
*and* agent rewriting both failed. If what exists today solves 8 or more of them, the request is
closed for good; if it doesn't, it reopens and those 10 cases are what it has to pass. There is no embedding table or column pre-wired in the code.

## Docs

- [`docs/architecture.md`](docs/architecture.md) — architecture and 41 key decisions **with their
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
