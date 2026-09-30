# CUDly CLI

CUDly is an open source CLI for discovering and purchasing AWS Reserved Instances and Savings Plans in a single command. It is dry-run by default: nothing is purchased until you pass `--purchase`. `configure-azure` and `configure-gcp` bootstrap credentials for the separate [self-hosted platform](https://github.com/LeanerCloud/cloud-commitments-platform); this CLI's own recommend-and-purchase workflow is AWS-only today. See [cloud setup](docs/cli/cloud-setup.md).

It is also built to be driven by an AI agent for the discovery and analysis side: searching recommendations, sizing a plan, filtering by account or region. Real purchases require `--purchase` and confirmation at an interactive terminal. The `--yes` bypass has been removed; scripts and agents should hand their dry-run recommendations to a human for review and purchase.

The CLI depends on the published shared Go modules in [cloud-commitments-go](https://github.com/LeanerCloud/cloud-commitments-go), pinned to fixed versions in `go.mod`. No sibling checkout or parent workspace is needed for local development.

## Key Features

- **Dry-run by default** - `--purchase` is the only opt-in that moves money; a bare invocation only prints results and writes a CSV.
- **Grounded recommendations** - sized from AWS Cost Explorer's own recommendation and coverage data, not a locally-guessed baseline.
- **Multiple AWS services, one interface** - RDS, ElastiCache, EC2, OpenSearch, Redshift, MemoryDB, and Savings Plans through the same command and flags. See [Implementation Status](#implementation-status) for per-service maturity.
- **Coverage control** - purchase a percentage of what's recommended, or of actual historical usage via `--target-coverage`, instead of buying everything a provider suggests in one run.
- **CSV + audit log** - every dry run and every purchase is written to CSV and to a permanent JSONL audit log.

## Safety Features

1. **Dry-run by default** - no purchase without the explicit `--purchase` flag.
2. **Confirmation prompt** - At an interactive terminal, `--purchase` prints the total instance count and estimated savings, then asks for confirmation once for the whole run. Nonterminal input is refused, including piped `yes`. Dry runs need no confirmation.
3. **Coverage and instance limits** - `--coverage`, `--target-coverage`, and `--max-instances` shape what a dry run recommends before there is anything to confirm.
4. **RDS extended-support filtering** - by default, recommendations for instances running an engine version in AWS Extended Support are excluded, since the surcharge can erase RI savings; pass `--include-extended-support` to include them.
5. **Audit log written per recommendation** - the audit log path is checked for writability before any cloud API call. Each recommendation then gets its own audit record: for a dry run, written as soon as its (local, no-API-call) result is generated; for a real purchase, written after that purchase call returns.
6. **Permanent CSV exports** of every dry run and every purchase.
7. **Duplicate-purchase dedup, fails closed** - every path (`--services` and `--input-csv`) subtracts commitments purchased within `--idempotency-window` (default `24h`, whole hours only; an invalid value is rejected at startup) before sizing a recommendation. If the existing-commitments API call itself fails, a dry run continues with a warning (nothing is bought); a `--purchase` run refuses that (service, region) with a "Refusing to purchase" line and buys nothing there.

Full internals: [Purchase Safety](docs/cli/purchase-safety.md).

## Implementation Status

| AWS service | Status |
|---|---|
| RDS, ElastiCache | Production - the tested paths. |
| EC2, OpenSearch, Redshift, MemoryDB, Savings Plans | Experimental - implemented and functional, still accumulating real-world purchase validation. |

Azure and GCP are not part of this CLI's recommend-and-purchase workflow; `configure-azure` and `configure-gcp` only bootstrap credentials for the [self-hosted platform](https://github.com/LeanerCloud/cloud-commitments-platform).

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

Use the AWS SDK's supported credential chain. Select a profile with `--profile` and validate access before a purchase.

- Recommendation data and purchase APIs can change outside this repository.
- See [Implementation Status](#implementation-status) for per-service maturity.
- `configure-azure` and `configure-gcp` bootstrap credentials for the self-hosted platform, not for this CLI - see [cloud setup](docs/cli/cloud-setup.md).

## Related components

- [Shared Go libraries](https://github.com/LeanerCloud/cloud-commitments-go) provide provider and common packages.
- [MCP server](https://github.com/LeanerCloud/cloud-commitments-mcp) exposes a separate tool interface.
- [Self-hosted platform](https://github.com/LeanerCloud/cloud-commitments-platform) owns the API, dashboard, and deployment paths.

This component owns the CLI under `cmd` and its CLI documentation. It does not own the web dashboard, MCP server, or deployment infrastructure.

## License and attribution

CUDly is maintained by [LeanerCloud](https://github.com/LeanerCloud) and licensed under the [Open Software License 3.0](LICENSE). See the repository license and attribution files for third-party notices.
