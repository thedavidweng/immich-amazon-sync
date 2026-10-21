# Amazon target contract

All Amazon operations use the public `amazon-photos/pkg/aphotos` SDK.
`NewWithClient` accepts an SDK client configured with its public HTTP test seam.
Production construction disables SDK retries, prevents credential redirects,
and leaves region inference and cookie validation to the SDK. A successful root
probe reports the SDK's existing region evidence without upgrading it.

Identity is the authenticated remote root ID, independent of cookies. A verified
root storage parent is represented as `bridge.Node.ParentID=""`; an explicit
other storage folder retains its ID. Confirmation requires FILE, AVAILABLE,
remote MD5, explicit size, account ownership when provided, and verified storage
parent. Upload uses the caller's original stream and context, then independently
reads GetNode and compares the staged MD5 and size.

Network, server, malformed-response and cancellation errors after a write are
unconfirmed. Callers must retain durable intent and reconcile before another
write. The adapter does not retry writes. Album creation never adopts a name
collision; an uncertain create cannot authorize recreation. Membership uses only
SDK collection operations, with stable album ID and account validation. There
is no media deletion operation.

The SDK pin `v0.1.3-0.20261021004957-6a7dc3ea2ff7` validates list envelopes.
Explicit `{"count":0,"data":[]}` results establish an empty search or album;
missing/null fields and partial pages cannot establish absence. Find requests
one bounded page and requires zero or one verified result. Multiple matches are
unconfirmed. Album members use pages of 200, with stable count, advancing offsets,
HasMore, ownership and duplicate-ID checks. No partial membership is returned.

The SDK currently has no ContentDate write capability. Original embedded EXIF
is preserved, while Immich-only timestamp edits are not transferred. SDK
RetryAfter is retained where exposed (currently RateLimitError). HTTP 503
RetryAfter and final non-2xx statuses below 400 for mutations/uploads remain
generic SDK gaps; read status validation is fixed in the pinned revision.

Tests are offline HTTP contracts, not live proof of regions, album behavior,
format support or presentation. No cloud credentials or live writes are used.
