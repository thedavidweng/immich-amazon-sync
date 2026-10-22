# 0014: Eligibility filters and reevaluable, non-destructive exclusions

Status: accepted. Date: 2026-10-22.

Eligibility decides whether unsettled media may be uploaded; it is not a
deletion or membership mechanism. Each account has one validated policy with
defaults identical to the released behaviour: `media_types` is `["image"]`,
archive visibility is included, and no byte limit, RAW skip or tag exclusion is
active. A migrated account keeps exactly that surface, and supplying both the
legacy `include_videos` toggle and `filters` is rejected with `filter_conflict`
instead of guessing which one wins.

Rejections are durable finite reasons — `media_type_excluded`, `raw_excluded`,
`size_excluded`, `size_unknown`, `archived_excluded`, `tag_excluded`,
`tag_provenance_unavailable` — stored in the existing asset row. A policy
fingerprint change requeues filter-managed exclusions without a full rescan, and
a refreshed hint or reconciliation reevaluates them from new metadata or tags.
Source conditions (`hidden`, `locked`, `offline`, `trashed`, `unsupported_type`,
checksum provenance) remain separate reasons and take precedence, so a policy
can never hide a source fact or resurrect trashed media.

An unknown source size is excluded as `size_unknown` when a limit is set, never
treated as zero bytes, and missing tag provenance excludes affected assets rather
than assuming they are untagged. A confirmed or Existing mapping survives a
policy change; filters never remove membership, never authorize trash and never
count absence as deletion evidence. This decision adds no second state store, no
remote query and no deletion path.
