# Archera recommendation quote

`cudly archera-recommendation-quote` is opt-in and read-only. It looks up the
Archera quote that belongs to one recommendation. Configure environment-only
`ARCHERA_API_KEY`, plus `ARCHERA_ORG_ID` and `ARCHERA_PLAN_ID` (or `--org-id`
and `--plan-id`). It performs no purchase, enrollment or audit write and is
independent of `--purchase`.

```bash
cudly archera-recommendation-quote --format json \
  --recommendation-id <uuid> --provider aws --service savings-plans-ec2instance \
  --plan-type EC2Instance --source-account <account-id> --offering-id <offering-id> \
  --region us-east-1 --instance-family m5 --term 1yr --payment no_upfront \
  --hourly-commitment 1.25
```

All identity flags are required and validated before any Archera request:
`--recommendation-id` and `--plan-id` are canonical UUIDs, `--term` is `1yr` or
`3yr`, `--payment` is `no_upfront`, `partial_upfront` or `all_upfront`, and
`--hourly-commitment` is a plain positive decimal string (no quotes, sign or
exponent). `--format` accepts `table` (default) or `json`.

A figure is printed only when exactly one Archera line item matches the
recommendation on every identity field and its comparison proves the same
hourly commitment. Figures are the vendor's exact decimal strings; an unknown
figure is shown as `unknown` (JSON null), never as zero. Otherwise the output
is `No Archera quote` with one status:

- `unavailable`: no exact line item or comparison basis could be proven
- `ambiguous`: more than one line item matches
- `ineligible`: the recommendation is not a product the matcher supports
  (only AWS EC2 Instance Savings Plans of one or three years); Archera is not
  called

A failed Archera read prints fixed text with the failure kind, the call stage,
the HTTP status and any Retry-After, never vendor text. The result is not a
bindable insurance quote, and no currency is supplied.
