# `axis tui`: the terminal workspace

`axis tui` opens a full-screen workspace. The agent conversation fills the
main pane, a sidebar shows cluster health, and the cluster dashboard is one
key away. It needs an interactive terminal; piped or redirected, it exits
with `axis tui requires an interactive terminal`.

```text
┌──────────────────────────────────────────────────┬───────────────────────┐
│ ┃ > which node has the most free RAM?            │ AXIS  v0.x.y          │
│ │ $ axis_status cluster status 11ms cached       │                       │
│   * node-a has the most free RAM (29 GB free).   │ CLUSTER               │
│                                                  │ ▲ 3/4 reachable       │
│                                                  │ 2m ago · daemon-cache │
│                                                  │ mem ━━━━━━━━━━━  63%  │
│                                                  │ NODES                 │
│                                                  │ ● node-a          54% │
│                                                  │ ✖ node-c         down │
│ ──────────────────────────────────────────────── │ ALERTS 2              │
│ ❯                                                │ ⚠ node-c unreachable  │
│ ── model: … ── context: … ── mode: observe ──    │ KEYS …                │
├──────────────────────────────────────────────────┴───────────────────────┤
│ AXIS agent            ctrl+o commands  ctrl+x leader  f2 dashboard  …    │
└──────────────────────────────────────────────────────────────────────────┘
```

## Starting it

| Command | Opens |
| --- | --- |
| `axis tui` | Workspace: agent + sidebar + dashboard |
| `axis tui --model <tag>` | Workspace with a pinned agent model |
| `axis tui --autonomy cluster` | Workspace; the agent may only read AXIS cluster state |
| `axis tui --dashboard` | The cluster dashboard alone, no agent |

`--autonomy` takes the same modes as `axis agent` (`cluster`, `default`,
`edit`, `full`). Run it in a real terminal. The `!` prefix of an agent CLI
does not give the program a terminal, so the TTY check refuses it.

## The conversation pane

This is the `axis agent` console, the same in-process agent and startup path,
hosted inside the workspace. Everything `axis agent` does works here:

- streaming answers, tool cards, reasoning summaries
- approval prompts for guarded actions (`y` approves; Enter does not)
- `/model`, `/skills`, `/mcp`, `/fleet`, `/usage`, `/clear` and the other
  slash commands; `!cmd` runs a shell command through guarded execution
- `@` completes a node name
- prompts typed while a turn runs are queued for the next turn

While the start screen is up, model resolution, MCP connections and the
snapshot load report progress there. Prompts typed before a model is ready
are held, not dropped.

Each transcript entry has a colored bar on its left edge:

| Bar | Entry |
| --- | --- |
| cyan `┃` | your prompt |
| none, indented | the agent's answer |
| faint `│` | a tool call and its result |
| faint `┊` | a reasoning block |
| yellow `┃` | an approval record |
| red `┃` | an error |

`pgup` / `pgdown` scroll half a pane and `shift+↑` / `shift+↓` one line. New
output follows the bottom unless you have scrolled up; a `↓ N more line(s)
below` marker says how far. The transcript re-wraps when the window resizes.
It keeps the most recent 5,000 entries; older ones are dropped from the view.
Unlike `axis agent`, nothing goes to the terminal's own scrollback, because
the workspace owns the whole screen.

## The sidebar

The sidebar shows the cluster from the cached snapshot (daemon cache first,
then `~/.axis/snapshot.json`): reachable/total nodes, snapshot age (yellow
after 5 minutes, red after 30), source, cluster memory, every node with its
RAM use or `down`, and the first alerts. It refreshes every 30 seconds. It
hides on terminals narrower than 100 columns; `ctrl+x b` toggles it.

## Keys

| Key | Action |
| --- | --- |
| `ctrl+o` | Command palette: type to filter, `↑`/`↓` to pick, Enter to run |
| `ctrl+x` then a key | Leader: `d` dashboard, `p` placement preview, `r` refresh, `b` sidebar, `m` model, `s` skills, `c` MCP, `f` fleet summary, `u` usage, `n` clear, `h` help, `q` quit |
| `F2` | Toggle the dashboard |
| `esc` | Cancel the running turn |
| `ctrl+c` | Clear the draft; on an empty prompt, press twice to quit |

Every palette command is also a leader key. The slash-command entries (`m`,
`s`, `c`, `f`, `u`, `n`) run the same slash command you could type; there is
no second command grammar.

## The dashboard

`F2` or `ctrl+x d` switches the whole screen to the dashboard: a node table
and an inspector for the selected node.

| Key | Action |
| --- | --- |
| `↑`/`↓` or `j`/`k` | Select a node; `g`/`G` first/last; `pgup`/`pgdown` page |
| `←`/`→`, `h`/`l`, `tab`, `1`–`4` | Inspector tab: Overview, Models, Storage, Reservations |
| `a` or Enter | Back to the conversation with `@<node> ` typed in the prompt |
| `p` | Placement preview (advisory; never runs a task) |
| `r` | Refresh now |
| `?` | Help |
| `esc` or `q` | Back to the conversation |

The table drops low-priority columns (GPU, then link, role, state) on narrow
terminals and scrolls when nodes outnumber rows (`1-10 of 13`).

## What the views claim

The sidebar and dashboard show only what the snapshot observed:

- A missing value reads `not observed`, never a guess. The link column names
  the observed network class (`lan`, `tailnet`, `vpn`, `relayed`, `unknown`).
- Unreachable nodes show their error, not empty gauges.
- The Reservations tab shows the reserved RAM the snapshot carries. Per
  reservation detail is `axis reservations list`.
- The header and sidebar always name the snapshot's source and age.

Agent output is advisory, as in `axis agent`: it reads the fact plane through
tools and is not cluster truth.

## Terminals

- **No Unicode** (`TERM=linux`, a `C`/`POSIX` locale): the dashboard and
  sidebar draw in pure ASCII.
- **`NO_COLOR`** or `--no-color`: no color.
- **Small windows:** every frame fits the terminal exactly; panels shrink or
  drop rather than wrap.

## Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `axis tui requires an interactive terminal` | Run it in a terminal, not through a pipe or an agent's `!` prefix |
| Sidebar says `no snapshot` | No daemon cache and no `snapshot.json`. `ctrl+x d` shows setup steps: `axis init`, then `axis daemon service install` or `axis daemon start` |
| Snapshot age is red | The daemon is not refreshing. Check `axis daemon status` |
| `no model` on the start screen | No reachable model. Use `/model`, or `axis model start` |
| Typed prompt does nothing | It is held until the model is ready, or queued behind a running turn; the console says which |

## For developers

The code is in `cmd/axis/tui` (workspace, dashboard, sidebar) and
`internal/console` (the conversation; embedded mode is `embedded.go`). The
design and its trade-offs are recorded in
[`docs/decisions/tui-agent-workspace.md`](../decisions/tui-agent-workspace.md).

```bash
make build && ./axis tui                                   # run your build
go test ./cmd/axis/tui ./internal/console                  # unit + golden tests
UPDATE_GOLDEN=true go test ./cmd/axis/tui -run Golden      # regenerate frames
```

Golden frames live in `cmd/axis/tui/testdata/`. Review their diff after
regenerating; they are the reviewable record of what each screen looks like.
