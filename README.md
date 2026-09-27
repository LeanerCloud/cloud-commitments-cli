# CUDly CLI

The CLI discovers cloud commitment recommendations and can purchase AWS Reserved Instances, Savings Plans, and selected Azure and GCP commitments. Amazon RDS and ElastiCache are the tested AWS service paths. Other AWS services, Azure, and GCP support remain experimental.

The CLI depends on the published shared Go modules in [cloud-commitments-go](https://github.com/LeanerCloud/cloud-commitments-go), pinned to fixed versions in `go.mod`. No sibling checkout or parent workspace is needed for local development.

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

Use `--purchase` to enable a purchase operation. Use `--yes` to skip its confirmation prompt. A terminal prompt is not an automation boundary. Read the purchase-safety guide before using this mode. The CLI's `--idempotency-window` flag does not prevent duplicate purchases in the CLI path; review the dry-run output and audit log before retrying.

Export a reviewable report when you need to share results:

```bash
./cudly --services rds --profile default --output recommendations.csv
```

All purchase operations can spend money. Check the account, region, quantity, and selected commitment before confirming.

## Credentials and provider status

Use the provider's supported credential chain. For AWS, select a profile with `--profile` and validate access before a purchase. Follow the cloud setup guide for Azure and GCP.

- Amazon RDS and ElastiCache are the tested AWS service paths.
- Other AWS service paths can change and are not covered by the same maturity claim.
- Azure and GCP support is experimental and can vary by service and account.
- Recommendation data and purchase APIs can change outside this repository.

## Related components

- [Shared Go libraries](https://github.com/LeanerCloud/cloud-commitments-go) provide provider and common packages.
- [MCP server](https://github.com/LeanerCloud/cloud-commitments-mcp) exposes a separate tool interface.
- [Self-hosted platform](https://github.com/LeanerCloud/cloud-commitments-platform) owns the API, dashboard, and deployment paths.

This component owns the CLI under `cmd` and its CLI documentation. It does not own the web dashboard, MCP server, or deployment infrastructure.

## License and attribution

CUDly is maintained by [LeanerCloud](https://github.com/LeanerCloud) and licensed under the [Open Software License 3.0](LICENSE). See the repository license and attribution files for third-party notices.
