# CLAUDE.md

## Behavioral Rules (Always Enforced)

- Do what has been asked; nothing more, nothing less
- NEVER create files unless they're absolutely necessary for achieving your goal
- ALWAYS prefer editing an existing file to creating a new one
- NEVER proactively create documentation files (*.md) or README files unless explicitly requested
- NEVER save working files, text/mds, or tests to the root folder
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

## Go Module Notes

- This project does NOT use a vendor directory. Do not use `go mod vendor`.
- The shared libraries and cloud providers come from `github.com/LeanerCloud/cloud-commitments-go`, pinned to fixed versions in `go.mod`. There are no `replace` directives and no sibling checkout is needed.
- Build and test with `make build` and `make test-unit` from the repository root.

## Build & Test

The whole repo is a single Go module rooted at the repository root; the main package lives in `cmd/`.

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

## Issue assignment — claim before you work (MANDATORY)

Several people (and their agents) work through this repo's issue queue in
parallel. GitHub issue assignment is the lock that keeps two of them from
fixing the same thing. The `~/.claude/agent-comms/` bus below only
coordinates agents on one machine; assignment is what coordinates people.

1. **Pick work in priority order.** Take the highest-priority open issue
   that nobody owns: priority/p0 through p3, then no priority label. Within
   a level, prefer higher urgency/\*, then higher impact/\*, then issues that
   unblock others, then smaller effort/\*. Break remaining ties oldest first.
   Skip status/blocked, status/needs-info, status/wontdo, and anything
   assigned, claimed or already covered by an open PR.

   ```bash
   for p in p0 p1 p2 p3; do
     echo "== priority/$p"
     gh issue list --repo LeanerCloud/cloud-commitments-cli --label "priority/$p" --limit 1000 \
       --search "no:assignee -label:status/blocked -label:status/needs-info -label:status/wontdo sort:created-asc" \
       --json number,title --jq '.[] | "#\(.number) \(.title)"'
   done
   echo "== no priority label"
   gh issue list --repo LeanerCloud/cloud-commitments-cli --limit 1000 \
     --search "no:assignee -label:status/blocked -label:status/needs-info -label:status/wontdo sort:created-asc -label:priority/p0 -label:priority/p1 -label:priority/p2 -label:priority/p3" \
     --json number,title --jq '.[] | "#\(.number) \(.title)"'
   ```

   GitHub search returns at most 1000 results whatever `--limit` says, and
   truncates silently. If a query returns exactly 1000, split it into
   non-overlapping windows (for example add `created:<2026-07-01` and
   `created:>=2026-07-01`) and run each. Issues with no `priority/*` label
   come after `p3`. The query only sees assignees, so open the issue and
   check its comments for a claim (step 2) too. Before claiming, also check
   that no open PR already addresses it:
   `gh pr list --repo LeanerCloud/cloud-commitments-cli --limit 1000 --search "<issue number> in:body"`
   (split it the same way if it returns 1000).

2. **Claim it before writing any code.** Post a claim comment, and also
   assign yourself if you have write access. Claims, takeovers and releases
   are all comments starting with a fixed marker (`Claiming:`,
   `Taking over:` or `Released:`), because comment timestamps are what
   decide who claimed first; `gh issue view` cannot show when someone was
   assigned. Agents that share one GitHub login can't tell their claims
   apart by author, so each ends every marker with its worker id, shown as
   `[<worker-id>]` below (leave it out if the login is yours alone), and
   compares claims by worker id; same-machine agents can also use the bus
   below.

   ```bash
   gh issue comment <N> --repo LeanerCloud/cloud-commitments-cli \
     --body "Claiming: working on this [<worker-id>]"
   gh issue edit <N> --repo LeanerCloud/cloud-commitments-cli \
     --add-assignee @me   # needs write access
   ```

   Then list the assignees and markers and work out who holds the issue:

   ```bash
   gh issue view <N> --repo LeanerCloud/cloud-commitments-cli --json assignees,comments \
     --jq '"assignees: \([.assignees[].login] | join(", "))",
           (.comments[] | select((.body | test("^(Claiming|Taking over|Released):"))
                                 or (.body | test("claimed by"; "i")))
            | "\(.createdAt) \(.author.login): \(.body | split("\n")[0])")'
   ```

   A claim is active when its owner's latest marker is `Claiming:` or
   `Taking over:`; a later `Released:` from them ends it. The earliest
   active claim wins. If that is not you, release it and pick another
   issue (your latest marker is what counts):

   ```bash
   gh issue comment <N> --repo LeanerCloud/cloud-commitments-cli \
     --body "Released: claimed by someone else first [<worker-id>]"
   gh issue edit <N> --repo LeanerCloud/cloud-commitments-cli --remove-assignee @me
   ```

   Skip the `--remove-assignee` when another worker on your shared login
   still holds an active claim: the assignment is that worker's too, and
   removing it would make the issue look unowned.

   An assignment with no marker comment (for example a maintainer
   assigning someone) still means the issue is taken, and so does an older
   free-form claim comment such as `claimed by <worker>`.

3. **Respect other people's claims.** An issue with an active claim or an
   assignee belongs to that person. Do not work on it, even if it looks
   stale. A maintainer's assignment or a maintainer's `Taking over:`
   overrides any claim; everyone else uses the flow below. If the issue
   has had no activity and no linked PR for 7 days, ask on the issue,
   mentioning the owner. Take it over once they agree or release it. If
   they have not replied after 3 more days, post
   `Taking over: no reply since <date> [<worker-id>]`. That marker is your
   claim and supersedes only the stale owner's claim: when you work out
   the earliest active claim as in step 2, ignore the stale owner, but any
   other active claim posted before your takeover still wins. Then move
   the assignment to yourself (or ask a maintainer to, if you lack write
   access):

   ```bash
   gh issue edit <N> --repo LeanerCloud/cloud-commitments-cli \
     --add-assignee @me --remove-assignee <old-owner>
   ```

4. **Link the PR.** Put `Closes #<N>` in the PR body so the issue and PR
   stay connected (and the PR labeling rule below can find the issue).

5. **Release what you drop.** If you stop working on an issue without a
   PR, post `Released: <where you got to> [<worker-id>]` as a new comment
   and unassign yourself (unless another worker on your shared login
   still holds an active claim)
   (`gh issue edit <N> --repo LeanerCloud/cloud-commitments-cli --remove-assignee @me`), so the next
   person can pick it up. If the issue turns out to be already fixed, say
   which PR or commit fixed it and ask a maintainer to close it.

Work that is not tracked by an issue yet (for example a larger UI or
integration effort) gets an issue first, assigned to whoever is doing it,
so it shows up as taken.

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

## CodeRabbit loop: iterate to silence (when required by the review gate)

Run this loop when CodeRabbit is the chosen review path, or when the
independent-review condition in "Review gate" below is not met. The full
rules live in `~/.claude/git-workflow.md` §"Post-PR review loop"
(§§3, 3a); read them. The minimum-viable loop for this project:

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

When the CodeRabbit loop applies (see Review gate) and you are
**delegating PR work to a subagent**, the prompt MUST include the
full CR loop, not stop at the first `@coderabbitai review` ping. A fork
that pushes the PR, pings CR, then exits leaves the CR threads
unresolved — same failure mode as forgetting the post-push CI watcher.
The subagent's exit criteria must be: "CR's most recent review has zero
Actionable items AND every Nitpick is either fixed or has a
justification reply on the thread", not "PR opened and CR pinged". When
in doubt, copy the iteration loop above (steps 2–6) into the fork
prompt verbatim.

## Review gate

Merge only at the reviewed SHA, and only when all of these cover it:

- An independent adversarial review of the full PR diff by an available
  capable model, including Codex, names the exact current head SHA.
- All actionable findings from any reviewer (independent review,
  CodeRabbit, CI) are resolved. CodeRabbit is optional when
  exact-revision local verification plus a thorough independent review
  cover the SHA; otherwise run the loop above. CI is freshly green on
  the SHA, mergeability is clean, and the reviewed head is unchanged.
- For runtime changes, local verification exercises the actual affected
  user path and data shape on macOS (Linux via CI; Windows out of scope).
  Realistic fixtures, mocks, recorded responses, or local integration may
  satisfy this gate.
  Label the evidence honestly and record real-account coverage gaps.
  For runtime changes, require regression fail-before/pass-after evidence
  where applicable, a fresh build, and relevant tests.
  For non-runtime changes, run checks relevant to the changed artifact.
- The verdict, the reviewed SHA and the local verification evidence are
  recorded on the PR itself.

If CodeRabbit is blocked only by quota or throttling, the remaining gates
still permit a normal merge. Record `CR waived: quota, adversarial review + local verification + green CI`
on the PR and track a retrospective review. Resolve all available
CodeRabbit actionable findings.

Merge normally with branch protections and coordination; never bypass
failing or required checks (no `gh pr merge --admin`, no `--no-verify`).
Verification never authorizes real purchases, deploys or the CLI's `--yes`.
Any new commit or rebase restarts the gate; a missing reviewer or
verification blocks the PR.

## PR labeling — mirror closing-issue labels (MANDATORY)

Every PR opened in this repo must carry the **same** triage labels as
the issue it closes — `priority/*`, `severity/*`, `urgency/*`,
`impact/*`, `effort/*`, `type/*`, plus `triaged` if the issue carries
it. Skipping this leaves PRs invisible to the same priority queries
that surface the issues, so an unlabeled PR is effectively
unreviewable in priority order.

Mechanics: apply the labels right after `gh pr create`, in the same
round:

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
