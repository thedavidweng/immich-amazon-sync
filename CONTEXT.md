# Product context

An independent, headless, one-way backup service:

`Immich (read only) -> durable SQLite queue -> aphotos SDK -> Amazon Photos`.

Rainforest remains an independent desktop application. This service never imports
Rainforest internals or shares its data. Media deletion is not implemented.

The implementation baseline is `docs/spec.md`. GitHub Issues track delivery.
