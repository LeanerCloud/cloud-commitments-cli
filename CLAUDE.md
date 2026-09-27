# Claude Code Configuration - RuFlo V3

## Behavioral Rules (Always Enforced)

- Do what has been asked; nothing more, nothing less
- NEVER create files unless they're absolutely necessary for achieving your goal
- ALWAYS prefer editing an existing file to creating a new one
- NEVER proactively create documentation files (*.md) or README files unless explicitly requested
- NEVER save working files, text/mds, or tests to the root folder
- Never continuously check status after spawning a swarm — wait for results
- ALWAYS read a file before editing it
- NEVER commit secrets, credentials, or .env files

## Planning (ALWAYS follow this process)

- **EVERY TIME** you create or update a plan, you MUST enter a review loop: thoroughly review the plan and fix any issues found. Repeat until 3 consecutive review passes find no issues. Do NOT skip this step — it is mandatory for all plans, no exceptions.
- In each loop iteration, print a summary of found issues before and after fixing them.

## File Organization

- NEVER save working files to the root folder; use the directories below
- `cmd/`: the single CLI main package (builds the `cudly` binary); Go unit
  tests live next to the code they test
- `docs/`: CLI reference and topic documentation
- `scripts/`: repository hook and helper scripts
- `.github/`: CI workflows (`ci.yml`, `pre-commit.yml`)

## Project Architecture

- Follow Domain-Driven Design with bounded contexts
- Keep files under 500 lines
- Use typed interfaces for all public APIs
- Prefer TDD London School (mock-first) for new code
- Use event sourcing for state changes
- Ensure input validation at system boundaries

### Project Config

- **Topology**: hierarchical-mesh
- **Max Agents**: 15
- **Memory**: hybrid
- **HNSW**: Enabled
- **Neural**: Enabled

## Go Module Notes

- This project does NOT use a vendor directory. Do not use `go mod vendor`.
- The shared libraries and cloud providers come from `github.com/LeanerCloud/cloud-commitments-go`, pinned to fixed versions in `go.mod`. There are no `replace` directives and no sibling checkout is needed.
- Build and test with `make build` and `make test-unit` from the repository root.

## Build & Test

The whole repo is a single Go module rooted at `cmd/`.

```bash
# Build
make build             # or: go build -o cudly ./cmd
                        # (go build ./... fails: cmd/ is the only main
                        # package and its default output name collides
                        # with the cmd directory itself)

# Test
go test ./...          # or: make test-unit

# Lint
make lint               # golangci-lint; also: make vet, make fmt
```

- ALWAYS run tests after making code changes
- ALWAYS verify build succeeds before committing

## Known Issues

This repository has no `known_issues/` directory. Deferred tech debt or
surfaced bugs found while working here go into this repository's GitHub
issues instead. See `CONTRIBUTING.md` under "Known Issues Sweep" for the
full convention (including the cross-component sweep in the platform repo).

## Post-push CI watcher (MANDATORY — even for one-line fix commits)

After **every** `git push` that publishes new commits to a PR branch on
this repo (including follow-up CodeRabbit-nitpick fixes), launch a
background watcher per workflow run **before** ending the turn. This is
the project-level reinforcement of the global rule in
`~/.claude/git-workflow.md` §Post-push CI watcher — CUDly's pre-commit
job alone takes 9–15 min and is silently broken by routine changes
(missing CI tools, pre-existing markdownlint debt, git-secrets
allowlist drift), so leaving a push unwatched routinely lets CI
failures sit overnight.

Mechanics:

1. After `git push`, list the runs the push triggered:

   ```bash
   gh run list --repo LeanerCloud/cloud-commitments-cli --commit "$(git rev-parse HEAD)" \
     --limit 10 --json databaseId,workflowName,status
   ```

2. For each run still in `queued`/`in_progress`, launch one background
   watcher script via `Bash` with `run_in_background: true` (do NOT use
   foreground polling — the main session must stay unblocked). The
   minimum viable watcher polls `gh run view <ID> --json status,conclusion`
   every 30s and on completion either reports success or dumps the failed
   step digest. A reusable template lives at
   `.claude/scripts/watch-ci-run.sh` if present, or write a one-shot to
   `/tmp/claude/watch-<sha>-<workflow>.sh`.

3. If a watcher reports failure, the same session investigates and
   pushes a fix on the same branch (which fires a fresh watcher round).
   Decisions that need a human go to PR comments; everything else is
   autonomous per the global rule.

Forgetting this rule has been a recurring failure mode in this
project. Before declaring a "pushed and done" turn complete, confirm
at least one `ci-watch-*` background task is armed.

## CodeRabbit loop — iterate to silence (MANDATORY)

CodeRabbit reviews this repo on every push to a PR branch. The full
rules live in `~/.claude/git-workflow.md` §"Post-PR review loop"
(§§3, 3a) — read them. The minimum-viable loop for this project:

1. After every push, ping `@coderabbitai review` on the PR (CR doesn't
   always re-review automatically; the explicit ping makes it
   deterministic).
2. Wait for the review (60–120s polling, soft-handle 429s).
3. Triage every Actionable / Outside-diff / Nitpick finding into:
   actionable-fix-now, dismiss-with-justification-on-thread, or
   genuine-nitpick-batch-into-one-fix-commit.
4. Push the fix(es), comment on the PR summarising what was addressed
   vs. dismissed (and why), end the comment with a fresh
   `@coderabbitai review` ping.
5. **Repeat until CR's most recent review has zero Actionable items
   AND every Nitpick is either fixed or has a justification reply.**
   "I fixed pass 1" is not loop-exit. CUDly PRs commonly run 3–6 CR
   passes before settling.
6. If a fix push triggers conflicts (`mergeStateStatus: DIRTY`), resolve
   them per `~/.claude/git-workflow.md` §3a — `git rebase
   origin/<base>`, atomic conflict resolution, `git push
   --force-with-lease`, post a rebase note on the PR, then continue
   the CR loop.

Forgetting this rule leaves CR threads silently unresolved and pushes
the triage burden onto the human reviewer.

**When delegating PR work to a subagent**: the prompt MUST include the
full CR loop, not stop at the first `@coderabbitai review` ping. A fork
that pushes the PR, pings CR, then exits leaves the CR threads
unresolved — same failure mode as forgetting the post-push CI watcher.
The subagent's exit criteria must be: "CR's most recent review has zero
Actionable items AND every Nitpick is either fixed or has a
justification reply on the thread", not "PR opened and CR pinged". When
in doubt, copy the iteration loop above (steps 2–6) into the fork
prompt verbatim.

## PR labeling — mirror closing-issue labels (MANDATORY)

Every PR opened in this repo must carry the **same** triage labels as
the issue it closes — `priority/*`, `severity/*`, `urgency/*`,
`impact/*`, `effort/*`, `type/*`, plus `triaged` if the issue carries
it. Skipping this leaves PRs invisible to the same priority queries
that surface the issues, so an unlabeled PR is effectively
unreviewable in priority order.

Mechanics — fold into the **same `gh pr create` round**, before pinging
CodeRabbit:

```bash
# Right after `gh pr create ...` returns the PR URL:
# Derive PR_NUM from the current branch context (avoids brittle hand-copying).
PR_NUM=$(gh pr view "$(git rev-parse --abbrev-ref HEAD)" --repo LeanerCloud/cloud-commitments-cli --json number --jq '.number')
ISSUE_NUM=<the issue this PR closes>

LABELS=$(gh issue view "$ISSUE_NUM" --repo LeanerCloud/cloud-commitments-cli --json labels \
  --jq '[.labels[].name | select(test("^(priority|severity|urgency|impact|effort|type)/")) ]
        + (if [.labels[].name] | any(. == "triaged") then ["triaged"] else [] end)
        | join(",")')

# Guard against empty $LABELS — gh pr edit --add-label "" fails, which would
# silently break this MANDATORY flow. If the closing issue has no triage
# labels in the selected classes, surface the gap deterministically instead.
if [ -n "$LABELS" ]; then
  gh pr edit "$PR_NUM" --repo LeanerCloud/cloud-commitments-cli --add-label "$LABELS"
else
  echo "WARN: issue #$ISSUE_NUM has no priority/severity/urgency/impact/effort/type labels"
  echo "      Triage the issue first, then re-run the label-mirror step."
  echo "      Surface this gap in the PR body or as a comment on issue #$ISSUE_NUM."
fi

# Verify
gh pr view "$PR_NUM" --repo LeanerCloud/cloud-commitments-cli --json labels \
  --jq '[.labels[].name] | sort | join(",")'
```

If the closing issue lacks the `triaged` label, do NOT apply
`triaged` to the PR — that would lie. Surface the gap in the PR body
(or as a comment on the issue) so the human can triage.

If a label doesn't yet exist in the repo (rare — the label set is
populated from the existing issue queue), `gh label create` it with
the same color/description as a sibling label BEFORE applying.

For PRs that close more than one issue (e.g., a PR that closes
`#A` and `#B`): take the **highest** `priority/*` and `severity/*`
across the issues; `union` the rest (`type/*`, `effort/*`, `impact/*`,
etc.). The PR represents the work for both, so it should be
discoverable under either filter.

Forgetting this rule has the same shape as forgetting the post-push
CI watcher: it silently breaks priority-ordered review and triage.
Before declaring a "PR opened and done" turn complete, confirm the
label set on the PR matches the closing issue's set (or the merged
set for multi-close PRs).

## Security Rules

- NEVER hardcode API keys, secrets, or credentials in source files
- NEVER commit .env files or any file containing secrets
- Always validate user input at system boundaries
- Always sanitize file paths to prevent directory traversal
- Run `npx @claude-flow/cli@latest security scan` after security-related changes

## Concurrency: 1 MESSAGE = ALL RELATED OPERATIONS

- All operations MUST be concurrent/parallel in a single message
- Use Claude Code's Task tool for spawning agents, not just MCP
- ALWAYS batch ALL todos in ONE TodoWrite call (5-10+ minimum)
- ALWAYS spawn ALL agents in ONE message with full instructions via Task tool
- ALWAYS batch ALL file reads/writes/edits in ONE message
- ALWAYS batch ALL Bash commands in ONE message

## Swarm Orchestration

- MUST initialize the swarm using CLI tools when starting complex tasks
- MUST spawn concurrent agents using Claude Code's Task tool
- Never use CLI tools alone for execution — Task tool agents do the actual work
- MUST call CLI tools AND Task tool in ONE message for complex work

### 3-Tier Model Routing (ADR-026)

| Tier | Handler | Latency | Cost | Use Cases |
| ------ | --------- | --------- | ------ | ----------- |
| **1** | Agent Booster (WASM) | <1ms | $0 | Simple transforms (var→const, add types) — Skip LLM |
| **2** | Haiku | ~500ms | $0.0002 | Simple tasks, low complexity (<30%) |
| **3** | Sonnet/Opus | 2-5s | $0.003-0.015 | Complex reasoning, architecture, security (>30%) |

- Always check for `[AGENT_BOOSTER_AVAILABLE]` or `[TASK_MODEL_RECOMMENDATION]` before spawning agents
- Use Edit tool directly when `[AGENT_BOOSTER_AVAILABLE]`

## Swarm Configuration & Anti-Drift

- ALWAYS use hierarchical topology for coding swarms
- Keep maxAgents at 6-8 for tight coordination
- Use specialized strategy for clear role boundaries
- Use `raft` consensus for hive-mind (leader maintains authoritative state)
- Run frequent checkpoints via `post-task` hooks
- Keep shared memory namespace for all agents

```bash
npx @claude-flow/cli@latest swarm init --topology hierarchical --max-agents 8 --strategy specialized
```

## Swarm Execution Rules

- ALWAYS use `run_in_background: true` for all agent Task calls
- ALWAYS put ALL agent Task calls in ONE message for parallel execution
- After spawning, STOP — do NOT add more tool calls or check status
- Never poll TaskOutput or check swarm status — trust agents to return
- When agent results arrive, review ALL results before proceeding

## V3 CLI Commands

### Core Commands

| Command | Subcommands | Description |
| --------- | ------------- | ------------- |
| `init` | 4 | Project initialization |
| `agent` | 8 | Agent lifecycle management |
| `swarm` | 6 | Multi-agent swarm coordination |
| `memory` | 11 | AgentDB memory with HNSW search |
| `task` | 6 | Task creation and lifecycle |
| `session` | 7 | Session state management |
| `hooks` | 17 | Self-learning hooks + 12 workers |
| `hive-mind` | 6 | Byzantine fault-tolerant consensus |

### Quick CLI Examples

```bash
npx @claude-flow/cli@latest init --wizard
npx @claude-flow/cli@latest agent spawn -t coder --name my-coder
npx @claude-flow/cli@latest swarm init --v3-mode
npx @claude-flow/cli@latest memory search --query "authentication patterns"
npx @claude-flow/cli@latest doctor --fix
```

## Available Agents (60+ Types)

### Core Development

`coder`, `reviewer`, `tester`, `planner`, `researcher`

### Specialized

`security-architect`, `security-auditor`, `memory-specialist`, `performance-engineer`

### Swarm Coordination

`hierarchical-coordinator`, `mesh-coordinator`, `adaptive-coordinator`

### GitHub & Repository

`pr-manager`, `code-review-swarm`, `issue-tracker`, `release-manager`

### SPARC Methodology

`sparc-coord`, `sparc-coder`, `specification`, `pseudocode`, `architecture`

## Memory Commands Reference

```bash
# Store (REQUIRED: --key, --value; OPTIONAL: --namespace, --ttl, --tags)
npx @claude-flow/cli@latest memory store --key "pattern-auth" --value "JWT with refresh" --namespace patterns

# Search (REQUIRED: --query; OPTIONAL: --namespace, --limit, --threshold)
npx @claude-flow/cli@latest memory search --query "authentication patterns"

# List (OPTIONAL: --namespace, --limit)
npx @claude-flow/cli@latest memory list --namespace patterns --limit 10

# Retrieve (REQUIRED: --key; OPTIONAL: --namespace)
npx @claude-flow/cli@latest memory retrieve --key "pattern-auth" --namespace patterns
```

## Quick Setup

```bash
claude mcp add claude-flow -- npx -y @claude-flow/cli@latest
npx @claude-flow/cli@latest daemon start
npx @claude-flow/cli@latest doctor --fix
```

## Claude Code vs CLI Tools

- Claude Code's Task tool handles ALL execution: agents, file ops, code generation, git
- CLI tools handle coordination via Bash: swarm init, memory, hooks, routing
- NEVER use CLI tools as a substitute for Task tool agents

## Multi-Agent Communication

When multiple Claude instances or agents work on this project concurrently, they coordinate through a shared filesystem bus at `~/.claude/agent-comms/`. **Read `~/.claude/multi-agent-comms.md`** for the full protocol.

Key rules:

- Post a `sync` message at session start, after completing work, and before ending
- Post an `intent` message **before committing** and wait ~5s for conflicts
- `claim` the test runner lock before running the full test suite
- Post a `result` after commits and test runs so other agents stay informed
- Check for recent messages when resuming work to avoid conflicts
- Lock `git-commit` and `git-push` resources for the duration of those operations

Directory structure: `~/.claude/agent-comms/{messages,locks,status}`

## Knowledge graph (graphify)

**This project uses the graphify knowledge graph at `graphify-out/` — always consult it for architecture/codebase questions, and (re)build it when missing or stale.**

Rules, in priority order:

1. **Before answering architecture or codebase questions**: read `graphify-out/GRAPH_REPORT.md` for god nodes + community structure, and `graphify-out/wiki/index.md` for a navigable summary. The graph often surfaces helpers/utilities that grep misses because names don't overlap.
2. **If `graphify-out/` is missing**: build it FIRST before doing any non-trivial exploration. Command (from the repo root):

   ```bash
   "${GRAPHIFY_PYTHON:-python3}" \
     -c "from graphify.watch import _rebuild_code; from pathlib import Path; _rebuild_code(Path('.'))"
   ```

   Set `GRAPHIFY_PYTHON` to your graphify venv's Python interpreter (`<graphify-checkout>/.venv/bin/python3`); falls back to `python3` if the package is installed system-wide. Runs for ~1–3 minutes on this repo; run it in the background (`run_in_background: true`) so you can start reading other things while it finishes.
3. **After modifying code files in a session**: re-run the same command to keep the graph current. The installed `PreToolUse` hook in `.claude/settings.json` handles this automatically for Write/Edit/MultiEdit, but the hook has a 5-second timeout — on a large edit batch the rebuild may be skipped; run the command manually in that case.
4. **Never edit code you haven't mapped** when the graph is available. Prefer graph-assisted navigation over raw grep for cross-cutting questions ("who calls X", "where is Y implemented", "what would break if I rename Z").

If `graphify` is not on PATH, locate your local install (typically `~/bin/graphify` or your venv's `bin/`). The `graphify claude install` subcommand re-registers the PreToolUse hook if it's been removed.

## Support

- Documentation: <https://github.com/ruvnet/claude-flow>
- Issues: <https://github.com/ruvnet/claude-flow/issues>
