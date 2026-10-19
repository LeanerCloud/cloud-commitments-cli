# Saved platform plan summary

`ri-helper platform-plan get <id> [--format table|json]` reads one saved
platform plan. The default format is table. The ID is the platform plan UUID,
not an Archera organization, plan or line-item ID or a provider commitment ID.

Set `CUDLY_PLATFORM_URL` to an HTTPS origin, with an optional trailing slash.
Set `CUDLY_PLATFORM_USER_API_KEY` to a previously issued, scoped platform user
API key with `view:plans` permission and access to the plan's accounts. The
server enforces permission and account scope. The client cannot identify the
issuance type of an arbitrary string; do not put an infrastructure admin key
in this variable. There are no credential flags or persisted client settings.
Archera, inbound server and provider credentials are never fallback values.

The command makes one GET `/api/plans/<id>` with `X-API-Key`, never a purchase
or modification. Help and invalid arguments/configuration make no request.
Redirects are refused and responses are bounded to 1 MiB. The request and
operation context use a 30-second deadline, shortened by an earlier caller
deadline, with cancellation checked before publication. A blocking stdout
write cannot be interrupted by this context.

Table and JSON expose only `id`, `name`, `enabled`, `auto_purchase`, `ramp_type`,
`current_step`, `total_steps`, `updated_at`, `next_execution_date` and
`last_execution_date`. This is the documented JSON allowlist. Null or absent
dates remain unknown (JSON null). Next execution is reported by the platform;
it may be calculated by the server rather than persisted. It is neither a
guarantee nor an execution reservation. Timestamps are not recomputed.

Names and ramp labels are terminal-cleaned and capped at 256 bytes. Before
display, whole-key reflection detection folds NFKD/lowercase and removes
controls, format/separator/combining runes and runes outside the normalized
key's alphabet. A matching whole normalized key refuses the entire result.
Short/numeric keys can conservatively collide with real fields. Inserting
additional runes inside the key alphabet can break this contiguous match;
the policy does not detect every semantic encoding or look-alike alphabet.
Rejection or cancellation observed before output starts emits no result.
Once a writer starts, cancellation or writer failure cannot retract bytes.

Full service configuration, monetary caps, ramp interval/start settings,
health, execution history, listing and all edits remain outside this summary.
No platform-plan MCP tool is supplied by this CLI feature.
