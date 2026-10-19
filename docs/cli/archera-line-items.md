# Archera line-item discovery

`cudly archera-line-items` is opt-in and read-only. Configure environment-only
`ARCHERA_API_KEY`, plus `ARCHERA_ORG_ID` and `ARCHERA_PLAN_ID` (or `--org-id`
and `--plan-id`). It performs no purchase, enrollment or audit write.

```bash
cudly archera-line-items --format json --offset 0 --limit 100
cudly archera-comparison --line-item-ids <discovered-vendor-uuid>
```

`--format` accepts `table` (default) or `json`. `--offset` defaults to 0 and
must be nonnegative; `--limit` defaults to 100 and accepts 1..200. Every call
fetches and validates **all vendor pages first**, within a 30-second aggregate
deadline. Offset equal to total returns an empty array; larger offsets fail.
Rows are sorted by case-insensitive UUID, preserving the original spelling.
Local pages repeat a complete read; they have no guaranteed atomic snapshot.

Both formats expose total, offset, limit and next offset (null/none at the end),
plus both existing disclosures. JSON `line_items` is always an array.
Rows contain vendor line_item_id and offer_id, is_selected, exact integer-text
selected_quantity, exact decimal-text selected_commitment, contract_term,
payment_option, provider, commitment_type, region, instance_family,
native_plan_type and exact decimal-text native_duration_seconds. Null offer
dimensions remain null (table: unknown); zero remains "0". Native duration is
distinct from effective contract term; commitment_type comes from contract_spec.

Selected commitment is hourly selection for Savings Plans and zero for RIs,
not monthly cost, savings or a quote. No currency is supplied. Account/org/plan
and unused vendor fields are omitted. IDs do not prove recommendation,
cross-account association or economic equivalence. Call comparison explicitly
with chosen vendor UUIDs; discovery never invokes it. Failures print no partial
rows; output above 512 KiB is rejected with guidance to reduce the local limit.
