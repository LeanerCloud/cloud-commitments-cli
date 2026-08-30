# CUDly MCP Server

`cudly-mcp` exposes CUDly's reserved-capacity search and purchase tools (AWS EC2/RDS/ElastiCache/OpenSearch/Redshift/MemoryDB/Savings Plans, Azure VM Reservations, GCP Compute Engine CUDs) to any MCP client -- Claude Code, Claude Desktop, or another MCP-speaking agent -- as a local process. It is a thin wrapper around the same in-process Go packages (`pkg/provider`, `pkg/common`) the `ri-helper` CLI uses; it never shells out to `ri-helper`, and it is not deployed anywhere (see [Deployment model](#deployment-model)).

Every purchase tool is dry-run by default (`dry_run=true`) and requires an explicit `confirm=true` alongside `dry_run=false` before it spends money. A real purchase additionally requires the operator to have started the server with `CUDLY_MCP_ENABLE_REAL_PURCHASES=1` -- unset by default, so a fresh install cannot spend money until the operator opts in. See [Safety model](#safety-model).

## Install

From the repository root:

```bash
go install ./cmd/cudly-mcp
```

This installs the `cudly-mcp` binary to `$(go env GOBIN)` (or `$(go env GOPATH)/bin` if `GOBIN` is unset); make sure that directory is on your `PATH` so `cudly-mcp` resolves without a full path.

Or run directly without a separate install step:

```bash
go run ./cmd/cudly-mcp
```

There is no separate module or release artifact for `cudly-mcp` yet -- install it from a checkout of this repository.

## Configure credentials

The server takes no CUDly-specific configuration of its own. Each provider tool authenticates the same way the corresponding CUDly CLI path does, and every tool call also accepts a per-call override (`aws_profile`, `azure_subscription_id`, `gcp_project_id`) so one running server instance can serve requests against different accounts/subscriptions/projects without a restart.

### AWS

One of, in the usual SDK precedence order:

- `AWS_PROFILE` (matches a profile in `~/.aws/config` / `~/.aws/credentials`)
- `AWS_ACCESS_KEY_ID` + `AWS_SECRET_ACCESS_KEY` (+ optional `AWS_SESSION_TOKEN`)
- An IAM role (EC2 instance profile, ECS task role, etc.)

Per-call override: pass `aws_profile` on any AWS tool call to use a specific named profile for that call only.

### Azure

One of:

- A service principal via `AZURE_CLIENT_ID`, `AZURE_CLIENT_SECRET`, `AZURE_TENANT_ID`
- Local `az login` state (picked up by `azidentity.NewDefaultAzureCredential`)

Set `AZURE_SUBSCRIPTION_ID` to select the subscription, or pass `azure_subscription_id` on a per-call basis to override it.

### GCP

One of:

- `GOOGLE_APPLICATION_CREDENTIALS` pointing at a service-account JSON key file
- Application Default Credentials (`gcloud auth application-default login`)

Pass `gcp_project_id` on a per-call basis to override the ambient project.

## Launch

```bash
cudly-mcp
```

The server speaks MCP over stdio and logs diagnostics to stderr; it does not print anything to stdout other than protocol traffic, so it is safe to launch directly from an MCP client's process-spawning config (below) rather than through a wrapper script.

By default, `cudly-mcp` cannot execute real purchases: `dry_run=false, confirm=true` calls are refused until you set `CUDLY_MCP_ENABLE_REAL_PURCHASES=1` in the environment the process launches with. Add it to the `env` block in [Register with an MCP client](#register-with-an-mcp-client) once you are ready to let this server spend money. See [Safety model](#safety-model) for the full rule.

## Register with an MCP client

Add an entry to your client's MCP server config. For Claude Code, this is `~/.claude/mcp.json`. If the client does not inherit your shell's `PATH`, use the absolute path `go install` reported: `$(go env GOBIN)/cudly-mcp` if `GOBIN` is set, otherwise `$(go env GOPATH)/bin/cudly-mcp` (`$(go env GOBIN)` expands to an empty string when `GOBIN` is unset, so that path alone is not a valid binary location):

```json
{
  "mcpServers": {
    "cudly": {
      "command": "/absolute/path/to/cudly-mcp",
      "env": {
        "AWS_PROFILE": "my-aws-profile",
        "AZURE_SUBSCRIPTION_ID": "00000000-0000-0000-0000-000000000000"
      }
    }
  }
}
```

`env` is optional -- omit it entirely to rely on whatever ambient credentials are already active in the shell that launches the client, or set only the provider(s) you actually use.

## Use with OpenAI Codex CLI

Codex CLI reads MCP server definitions from `~/.codex/config.toml`, under a `[mcp_servers.<name>]` table -- same `command`/`args`/`env` shape as Claude Code's `mcpServers` JSON, different file format. A project-scoped `.codex/config.toml` works the same way, but only if you have marked that project directory as trusted in Codex CLI -- an untrusted project's `.codex/` layer is silently ignored, so if `cudly` doesn't show up, register it in `~/.codex/config.toml` instead (or trust the project).

```toml
[mcp_servers.cudly]
command = "/absolute/path/to/cudly-mcp"
args = []

[mcp_servers.cudly.env]
AWS_PROFILE = "my-aws-profile"
AZURE_SUBSCRIPTION_ID = "00000000-0000-0000-0000-000000000000"
```

`CUDLY_MCP_ENABLE_REAL_PURCHASES` is deliberately omitted from this example -- leave it unset until you have exercised the dry-run path against this client and are ready to let it spend money; see [Safety model](#safety-model).

## Worked example

A typical session searches for a recommendation, previews the purchase, then executes it:

1. **Search**: call `cudly_search_recommendations` with `provider="aws"`, `service="ec2"`, `region="us-east-1"` to see what AWS Cost Explorer currently recommends reserving.
2. **Preview**: take a result's `region`/`resource_type`/`count` and call `cudly_aws_ec2_ri_purchase` with those values and `term_years`/`payment_option` of your choice. Leave `dry_run` at its default (`true`) -- the response validates your parameters without contacting AWS or spending anything, and reports `cost`/`on_demand_cost`/`estimated_savings`/`savings_percentage` only when a real figure is actually known (omitted otherwise, never a fabricated `0`).
3. **Execute**: once the preview looks right, call the same tool again with `dry_run=false, confirm=true`. This is the only combination that performs a real purchase, and only when the operator has also enabled `CUDLY_MCP_ENABLE_REAL_PURCHASES=1` on the server process; any other combination either previews or returns an explicit refusal error (see [Safety model](#safety-model)).

Every other provider's purchase tool (`cudly_aws_savingsplans_purchase`, `cudly_aws_rds_ri_purchase`, `cudly_azure_compute_ri_purchase`, `cudly_gcp_computeengine_cud_purchase`, ...) follows the identical dry_run-then-confirm pattern. Call `cudly_list_commitment_actions` at any point for the full, always-current list of tools, which ones can spend real money today, and 2-3 example prompts per tool.

## Safety model

- `dry_run` defaults to `true` on every purchase tool. A dry-run call never contacts the cloud provider and never spends money -- it only validates your parameters. It reports pricing (`cost`/`on_demand_cost`/`estimated_savings`/`savings_percentage`) only when a real figure is genuinely known; those fields are omitted, not zeroed, when it isn't.
- A real purchase requires **both** `dry_run=false` **and** `confirm=true`. `dry_run=false` with `confirm=false` (or vice versa) is refused with a structured error, not silently downgraded to a preview or silently ignored.
- A real purchase **also** requires the operator to have set `CUDLY_MCP_ENABLE_REAL_PURCHASES=1` (or `true`, case-insensitive) in the environment `cudly-mcp` was launched with. This is layered underneath `confirm`: `confirm` only proves the model asked to spend money, it does not prove the operator running this server wants it able to. The gate is unset (disabled) by default -- unset, empty, `0`, `false`, or any other value all disable real purchases -- so a fresh install cannot spend money until the operator explicitly opts in. When disabled, a `dry_run=false, confirm=true` call is refused before any provider or credential is touched, naming the flag to set. Dry runs are unaffected by this flag; they never spend regardless of its value.
- A real purchase requires the **target account to be named**: `aws_profile` (AWS) or `azure_subscription_id` (Azure), either as the tool argument or via the matching environment variable the provider itself reads (`AWS_PROFILE`, `AZURE_SUBSCRIPTION_ID`). **GCP has no environment fallback, so `gcp_project_id` is required for a real CUD purchase.** That is deliberate: nothing in `providers/gcp` reads `GOOGLE_CLOUD_PROJECT` or `CLOUDSDK_CORE_PROJECT`, and with no project configured the provider falls back to the *first active project* in your `ListProjects` response -- an artifact of IAM visibility and API ordering rather than a project anyone chose, which is not a defensible default for spending money. Pointing the scope at an environment variable the provider ignores would be worse still: the idempotency token would name one project while the commitment landed in another. If neither is set, the real purchase is refused before any credential is touched, naming the argument to pass. **Dry runs do not require it** -- you can price a purchase without naming an account. This is not bookkeeping: the account is folded into the idempotency token, so leaving it to ambient credentials on one call and naming it explicitly on the next derives two *different* tokens for the *same* account. Every provider's dedupe is token-keyed, so the second call's lookup would miss and buy a second commitment. Refusing an undeterminable account makes that aliasing unreachable. Naming an account explicitly and inheriting the same value from the environment always agree, so they still dedupe normally.
- Every money-affecting parameter (region, resource type, count, term, payment option, and any provider-specific dimension such as RDS's `az_config`) is validated against an explicit enum or non-empty check before anything is built or sent. There is no silent default for a value that materially changes what gets purchased.
- Every real purchase is tagged with a source identifying it came from this MCP server (never a user-suppliable string) and a deterministic idempotency token derived from the request's own parameters. By default, retrying an identical tool call -- however long after the original, and regardless of any clock boundary -- always derives the same token, so the provider dedupes the retry instead of buying twice; this is a fail-safe default, since the worst case of a false dedupe is a skipped intentional repeat, never a double purchase. To deliberately make a second, otherwise-identical purchase (e.g. "buy 3 RIs now" and "buy 3 more next week"), pass a fresh `idempotency_nonce` value on the second call; passing the same nonce on a retry of that same call still dedupes correctly.
- Provider/SDK failures surface their full error text back to the caller; nothing is swallowed.
- A **completed** real purchase carries an `archera` block in the response: an optional underutilization-insurance offer (Archera covers the gap if committed capacity goes unused), the signup link, the enrollment window in days, and both partnership disclosures. It is attached only when the purchase actually succeeded, never to a dry run or a failed purchase, because neither started an enrollment window. Archera sponsors CUDly's development from a fraction of their insurance premiums, and CUDly works fully without it; both facts travel with the link in every response so a client rendering this payload cannot present the offer as a neutral recommendation.
- Every real purchase writes an `mcp purchase ATTEMPT` line and a matching `mcp purchase OK` / `mcp purchase FAILED` line to **stderr**, recording provider, target account, region, resource, count, term, payment option, the resulting commitment ID, and a masked idempotency token. Dry runs emit no purchase diagnostic to stderr because they spend nothing, but when auditing is enabled and the append succeeds, they are persisted in the JSONL audit log as `status: "skipped"`. Capture your MCP client's stderr if you want the diagnostic trail retained. Nothing is written to stdout, which the MCP stdio transport owns for JSON-RPC framing.

### What this server does NOT give you

Understand these before enabling real purchases, especially in a shared or production account:

- **No scheduled/4-eyes approval workflow.** The web UI routes a purchase through `purchase_executions` with a scheduled date and, under 4-eyes mode, a second approver who cannot be the creator. This server has no such workflow: once `CUDLY_MCP_ENABLE_REAL_PURCHASES=1` is set, `dry_run=false` plus `confirm=true` in a single tool call executes immediately. `confirm` is still a guardrail against an accidental call rather than an authorization control -- it is supplied by the model driving the client, not the operator -- but `CUDLY_MCP_ENABLE_REAL_PURCHASES` is the operator-side authorization control layered underneath it: it must be explicitly enabled before *any* tool call, confirmed or not, can spend money on this server.
- **The persisted audit record is a local JSONL file, not CUDly's purchase history.** When auditing is enabled and a write succeeds, each purchase attempt -- including previews -- is appended as one `common.AuditRecord` JSON line to the path from [Audit log](#audit-log) below. An MCP purchase still does not appear in the CLI/web paths' `purchase_history`, so reconcile against the provider's console/billing data or this file rather than against CUDly's own database.
- **Credentials are whatever launched the process.** `aws_profile` / `azure_subscription_id` / `gcp_project_id` are per-call arguments chosen by the model, so any account reachable from the ambient credentials is reachable from any tool call. Scope the credentials you launch `cudly-mcp` with to what you are willing to let it spend, rather than relying on the tool arguments to constrain it.

## Audit log

Auditing is **on by default**. When auditing is enabled and a write succeeds, each purchase attempt -- previews included -- appends one `common.AuditRecord` JSON line to a local file, independent of the stderr trail in [Safety model](#safety-model) above.

- **Default path**: `$XDG_STATE_HOME/cudly/mcp-audit.jsonl`, falling back to `~/.local/state/cudly/mcp-audit.jsonl` when `XDG_STATE_HOME` is unset or empty.
- **`CUDLY_MCP_AUDIT_LOG`** overrides the path. Setting it to an **empty or whitespace-only string disables the log entirely** -- that is the explicit opt-out; leaving the variable unset is not the same thing and still uses the default path.
- A preview (`dry_run=true`) is recorded with `status: "skipped"`, `dry_run: true` -- it spent nothing, but it was still a decision worth reconstructing later.
- When a credential scope is supplied, `credential_scope` records that routing identifier: an AWS profile, Azure subscription, or GCP project. It identifies how CUDly selected the target; it is not a verified provider account ID. Previews may omit it because they do not require a target.
- A real purchase is recorded `"success"` only when the provider reports `Success: true` with no embedded error. A failed provider call, `Success: false`, or a result containing an error is recorded as `"error"`.
- Every purchase in one server process shares a single `run_id`, so a log spanning many purchases can be grouped by session.
- **A write failure never changes the purchase result.** It logs a warning to stderr naming the path and error; the tool response returned to the caller is unaffected.
- The path is probed for read, append, and directory durability at server startup. MCP creates missing parent directories as owner-only `0700` subject to umask, then opens and syncs every directory edge from the filesystem root. Each ancestor therefore needs search and read permission, every creation parent also needs write permission, and the filesystem must support directory `fsync`. A path that fails these checks stops server construction instead of silently dropping every record for the session.

## Caveats and known gaps

These are pre-existing behaviours in the underlying purchase clients, not something introduced by or specific to the MCP server -- flagged here so you know what to expect:

- **Azure VM Reservations have no partial-upfront billing plan.** Azure honors exactly two billing plans, all-upfront and no-upfront (billed monthly, same total price -- Azure charges no premium for spreading payments), and `cudly_azure_compute_ri_purchase` defaults `payment_option` to no-upfront when omitted. `payment_option=partial-upfront` has no Azure equivalent and is rejected with an explicit error rather than silently purchased under all-upfront or no-upfront instead.
- **An Azure purchase with no `payment_option` now fails loud instead of silently billing all-upfront.** `reservations.BillingPlanForPaymentOption` used to fall through an empty payment option to Azure's own implicit default, which is Upfront -- charging the entire commitment immediately even though nobody asked for that schedule. It now returns an explicit error naming the missing value. This is reachable outside the MCP server: migration `000032` added `recommendations.payment_option` as `TEXT NOT NULL` defaulting to the empty string, and `internal/purchase/execution.go` passes `rec.Payment` through without a non-empty check, so **scheduled purchases created from rows predating that migration will now fail rather than silently charge upfront.** That is the intended trade (an unrequested full-upfront charge is worse than a refusal), but it is a live behaviour change: if a scheduled Azure execution starts failing with "no payment option was supplied", set the row's `payment_option` explicitly and re-run. The MCP tool itself is unaffected -- `cudly_azure_compute_ri_purchase` defaults `payment_option` to no-upfront before the client is reached.
- **GCP Compute Engine CUDs commit resources, not instances.** `cudly_gcp_computeengine_cud_purchase` takes `vcpu_count` and `memory_gb` directly (a CUD is a vCPU+memory commitment), not an instance count -- there is no implicit vCPU-per-instance conversion.
- **AWS Savings Plans searches require term/payment/lookback; `cudly_search_recommendations` defaults them.** Unlike EC2/RDS reservation searches, `GetSavingsPlansPurchaseRecommendation` rejects the call unless all three are set. When `service` targets a Savings Plans search on AWS, the tool fills in unset fields with `term_years=1` (1yr), `payment_option=no-upfront`, `lookback_period=30d` -- an explicitly supplied value is never overridden. A Savings Plans search therefore resolves to exactly one (term, payment, lookback) triple and issues exactly one Cost Explorer call.
- **Reservation searches fan out over every term and payment option when you omit them.** `GetReservationPurchaseRecommendation` accepts exactly one `TermInYears` and one `PaymentOption` per request and returns recommendations only for that cell -- there is no "give me every variant" mode. So when `term_years` and/or `payment_option` are omitted on an EC2/RDS/ElastiCache/etc search, the tool expands the omitted dimension to its full menu and issues one call per combination (6 when both are omitted, 2 or 3 when one is), returning the concatenated results. Each returned recommendation carries the `term`/`payment_option` of the request that produced it, so its money figures are attributable to a specific offer. If any one combination fails, the whole search fails rather than returning a partial menu -- five of six offers is indistinguishable from "these are all your options". Note that `lookback_period` is NOT fanned out: omitting it leaves Cost Explorer's own server-side default (7 days) in place, since the lookback window is the usage evidence behind an offer rather than another offer to choose from. The fan-out multiplies the API bill along with the coverage: `cudly_search_recommendations` buys no commitment, but Cost Explorer bills per request, so an omitted-term/omitted-payment search costs six requests rather than one (more with pagination). The tool description states this too, so a model does not treat searching as free and re-run it in a loop.
- **`region` on `cudly_search_recommendations` is a client-side post-filter for AWS reservation/Savings Plans searches.** `GetReservationPurchaseRecommendation` and `GetSavingsPlansPurchaseRecommendation` are account-level Cost Explorer calls with no region parameter -- AWS returns recommendations from every region the account has usage in. The tool filters the returned recommendations down to `region`/`include_regions` (minus `exclude_regions`) after the call; when no region constraint is supplied, all regions are returned as before.

## Deployment model

`cudly-mcp` is a local/desktop process, not a deployed service: it is intentionally kept out of `iac/`, `terraform/`, and the `internal/api` Lambda packaging path. Run it on the same machine as your MCP client.

## Troubleshooting

- **"provider ... is not configured" / credential errors**: confirm the relevant environment variable(s) from [Configure credentials](#configure-credentials) are set in the shell (or the client's `env` block) that launches `cudly-mcp`, or pass the matching per-call override (`aws_profile` / `azure_subscription_id` / `gcp_project_id`).
- **Azure/GCP purchase calls appear to hang**: Azure Reservations and GCP Compute Commitments both provision asynchronously after the purchase call returns; a `success=true` response means the purchase request was accepted, not necessarily that the resource is already active in the portal/console. Re-run `cudly_search_recommendations` or check the provider console if you need to confirm activation state.
- **Rate limits / throttling from the cloud provider**: retry the same tool call with the same parameters -- the idempotency token guarantees a retry cannot double-purchase no matter how long you wait before retrying (see [Safety model](#safety-model)). If you genuinely want a second, separate purchase with the same parameters instead of a retry, pass a fresh `idempotency_nonce` value.
- **"invalid ... must be one of ..." errors**: every enum-typed parameter (term, payment option, engine, az_config, sp_type, scope, tenancy, platform) is validated against an explicit allow-list; call `cudly_list_commitment_actions` or re-check this README's per-tool schema for the exact accepted values.
