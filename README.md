# CUDly CLI

CUDly is an open source CLI for discovering and purchasing cloud commitments — AWS Reserved Instances and Savings Plans, plus selected Azure and GCP commitments — in a single command. It is dry-run by default: nothing is purchased until you pass `--purchase`.

It is also built to be driven by an AI agent for the discovery and analysis side: searching recommendations, sizing a plan, filtering by account or region. The purchase step still needs a human to review the numbers before committing money. **`--yes` currently skips the confirmation prompt outright, including for a non-interactive caller** (a script, a CI job, an agent driving the CLI as a subprocess) — see [Safety Features](#safety-features) and [#1943](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1943) before wiring `--purchase --yes` into anything unattended.

The CLI depends on the published shared Go modules in [cloud-commitments-go](https://github.com/LeanerCloud/cloud-commitments-go), pinned to fixed versions in `go.mod`. No sibling checkout or parent workspace is needed for local development.

## Key Features

- **Dry-run by default** - `--purchase` is the only opt-in that moves money; a bare invocation only prints results and writes a CSV.
- **Grounded recommendations** - built from the cloud provider's own recommendation APIs (AWS Cost Explorer, Azure Advisor, GCP recommender), not estimated locally.
- **Multi-cloud, one interface** - AWS, Azure, and GCP through the same command and flags. See [Implementation Status](#implementation-status) for per-provider maturity.
- **Coverage control** - purchase a percentage of what's recommended, or of actual historical usage via `--target-coverage`, instead of buying everything a provider suggests in one run.
- **CSV + audit log** - every dry run and every purchase is written to CSV and to a permanent JSONL audit log.

## Safety Features

1. **Dry-run by default** - no purchase without the explicit `--purchase` flag.
2. **Confirmation prompt** - `--purchase` prints a summary of instance count and estimated savings, then prompts for confirmation. `--yes` skips this prompt, including for a non-interactive caller - it is not currently an automation boundary. [#1943](https://github.com/LeanerCloud/cloud-commitments-cli/issues/1943) tracks closing that gap.
3. **Coverage and instance limits** - `--coverage`, `--target-coverage`, and `--max-instances` shape what a dry run recommends before there is anything to confirm.
4. **Instance type validation** - every recommendation is checked against known, valid instance types before it is shown.
5. **Full audit trail** - every recommendation, purchased or not, is written to the audit log before any purchase API call runs.
6. **Permanent CSV exports** of every dry run and every purchase.
7. **No CLI-side duplicate-purchase prevention yet** - `--idempotency-window` is accepted but has no effect on the CLI path; deduplication only runs in the self-hosted platform's server-side scheduler. Review the dry-run CSV and audit log before retrying a run.

Full internals: [Purchase Safety](docs/cli/purchase-safety.md).

## Implementation Status

| Cloud | Status |
|---|---|
| AWS | Production - Amazon RDS and ElastiCache are the tested paths; other AWS services remain experimental. |
| Azure | Experimental - selected commitment types; maturity can vary by service and account. |
| GCP | Experimental - selected commitment types; maturity can vary by service and account. |

## Build

Use the Go version declared in `go.mod`.

```bash
make build
./cudly --help
```

`make build` creates `./cudly` from `./cmd`. The build does not deploy or configure a cloud account.

Read the [CLI reference](docs/cli/README.md) for commands. See the guides for [cloud setup](docs/cli/cloud-setup.md), [filtering](docs/cli/filtering.md), and [purchase safety](docs/cli/purchase-safety.md).

## Common workflows

Preview RDS recommendations before enabling a purchase:

```bash
./cudly --services rds --profile default
```

Export a reviewable report when you need to share results:

```bash
./cudly --services rds --profile default --output recommendations.csv
```

All purchase operations can spend money. Check the account, region, quantity, and selected commitment before confirming - see [Safety Features](#safety-features) for what is and is not enforced today.

## Credentials and provider status

Use the provider's supported credential chain. For AWS, select a profile with `--profile` and validate access before a purchase. Follow the cloud setup guide for Azure and GCP.

- Recommendation data and purchase APIs can change outside this repository.
- See [Implementation Status](#implementation-status) for per-cloud maturity.

## Related components

- [Shared Go libraries](https://github.com/LeanerCloud/cloud-commitments-go) provide provider and common packages.
- [MCP server](https://github.com/LeanerCloud/cloud-commitments-mcp) exposes a separate tool interface.
- [Self-hosted platform](https://github.com/LeanerCloud/cloud-commitments-platform) owns the API, dashboard, and deployment paths.

This component owns the CLI under `cmd` and its CLI documentation. It does not own the web dashboard, MCP server, or deployment infrastructure.

## License and attribution

CUDly is maintained by [LeanerCloud](https://github.com/LeanerCloud) and licensed under the [Open Software License 3.0](LICENSE). See the repository license and attribution files for third-party notices.
