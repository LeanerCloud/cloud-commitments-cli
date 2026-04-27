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

- NEVER save to root folder — use the directories below
- Use `/src` for source code files
- Use `/tests` for test files
- Use `/docs` for documentation and markdown files
- Use `/config` for configuration files
- Use `/scripts` for utility scripts
- Use `/examples` for example code

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
- The `pkg/` directory is a separate Go module (`github.com/LeanerCloud/CUDly/pkg`) with a `replace` directive in the root `go.mod`.
- Build and test normally with `go build ./...` and `go test ./...` from the root.
- Run `go test ./pkg/...` from the `pkg/` directory when working on the submodule.

## Build & Test

```bash
# Build
npm run build

# Test
npm test

# Lint
npm run lint
```

- ALWAYS run tests after making code changes
- ALWAYS verify build succeeds before committing

## Security Rules

- NEVER hardcode API keys, secrets, or credentials in source files
- NEVER commit .env files or any file containing secrets
- Always validate user input at system boundaries
- Always sanitize file paths to prevent directory traversal
- Run `npx @claude-flow/cli@latest security scan` after security-related changes

## CI/CD IAM — bootstrap vs runtime split

The per-cloud `terraform/environments/*/ci-cd-permissions/` modules provision
the CI/CD deploy identities and are **applied once, manually, by a privileged
human** — not by the CI workflow itself. The main deploy workflow assumes a
deploy SA already exists and only has permission to manage workloads. Keep
this split when adding new IAM:

- **Bootstrap-only permissions** (AWS `iam:*`, Azure RBAC role assignments,
  GCP `roles/iam.roleAdmin`, `roles/resourcemanager.projectIamAdmin`,
  `roles/cloudkms.admin`) live in `ci-cd-permissions/`. They let the deploy
  SA manage its own downstream grants but are not granted to anything
  ephemeral.
- **Runtime permissions** for the Lambda / Cloud Run / Container App service
  accounts are defined in the per-cloud compute module (`modules/compute/
  {aws,gcp,azure}/...`) with the **narrowest possible scope**. Prefer custom
  roles (GCP `google_project_iam_custom_role`) or prefixed resource ARNs
  (AWS `arn:aws:iam::*:role/{prefix}*`) over broad predefined roles like
  `roles/compute.admin` or `Resource = "*"`.
- **No silent fallbacks to over-privileged roles.** If a runtime grant
  requires a bootstrap permission the deploy SA doesn't have, the apply
  SHOULD 403 — that's the signal to re-run the bootstrap, not to paper over
  with a wider grant. Fallback flags are allowed only as short-term
  workarounds and must be removed once the bootstrap has been re-applied.
- **GCP WIF attribute_condition** in `ci-cd-permissions/github_oidc.tf`
  restricts which branch can impersonate the deploy SA. Re-applying the
  module with a different `deploy_ref` (or the default) resets the
  condition. Pin `deploy_ref` in `terraform.tfvars` (gitignored, per-env)
  to avoid silently locking out the current feature branch.

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
