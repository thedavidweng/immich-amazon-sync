# Glossary

| Term | Meaning |
| --- | --- |
| Account | A configured source identity and Amazon destination with isolated state. |
| Rule | An explicit source album ID or an explicitly enabled whole-library scope. |
| Asset | An Immich original identified by account and stable asset ID. |
| Node | An Amazon media object confirmed by ID, MD5 and byte size. |
| Existing | A verified node reused from elsewhere in the same Amazon account; backed up, but not at the target parent. |
| Deletion pending | Immich reports the asset trashed; the mapped node awaits an explicit deletion pass. |
| Deleted | The mapped node was moved to the Amazon trash; recoverable there, terminal for this service. |
| Unconfirmed | A write may have committed; reconcile before considering another write. |
| Reconcile | Read source or destination state to recover missed or uncertain work. |
| Backfill | Low-priority discovery of historical source assets. |
| Membership | A relationship between an album and a media node, separate from media. |
