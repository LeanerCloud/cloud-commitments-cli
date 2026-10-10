# Archera plan comparison

`ri-helper archera-comparison` prints a read-only comparison for one Archera
commitment plan. It never purchases, binds or submits anything, it is
independent of `--purchase`, and it makes no Archera request unless you run it.

## Configuration

The feature is off until configured. There is no flag for the API key.

| Setting | Source | Secret |
|---------|--------|--------|
| `ARCHERA_API_KEY` | environment only | yes |
| `ARCHERA_ORG_ID` | `--org-id` or environment | no |
| `ARCHERA_PLAN_ID` | `--plan-id` or environment | no |

The org and plan IDs are Archera UUIDs, not CUDly recommendation IDs. If a
setting is missing, the command exits with status 1 and names the missing
setting. It never prints a value.

## Usage

```bash
export ARCHERA_API_KEY=...        # keep out of shell history and CI logs
export ARCHERA_ORG_ID=<org-uuid>
export ARCHERA_PLAN_ID=<plan-uuid>
ri-helper archera-comparison                 # table
ri-helper archera-comparison --format json   # one JSON document on stdout
```

Errors go to stderr and the exit status is 1 for every failure. A vendor error
shows the sanitized HTTP status and message, plus `Retry-After` when given
(capped at 24 hours). The command does not retry.

## Reading the output

- Totals include the Archera premium. The API reports no currency, so amounts
  are unlabelled.
- Monthly figures are 730-hour monthly rates. Upfront figures are one-time and
  are never added to monthly figures.
- Unknown values print `unknown` in the table and `null` in JSON, never `0`.
- Money is printed as an exact decimal. `discount_rate` is shown as sent
  (0-1 basis).
- The comparison is a hypothetical rollup for the whole plan. It is not a
  bindable quote, not a guarantee, and not mapped onto recommendation rows or
  CSV output.
- Product support is `supported` only when Archera's documentation lists the
  commitment type; otherwise `unknown`.
- Archera sponsors CUDly development. The command prints the same two
  disclosures as the post-purchase message, in both formats
  (`non_gating_disclosure` and `sponsorship_disclosure` in JSON).
