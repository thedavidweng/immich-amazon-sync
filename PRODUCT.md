# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

A self-hoster who runs Immich and keeps a second, one-way copy of their original media in Amazon Photos. Usually one account. They open the local dashboard mostly to glance and confirm backups are healthy, then close it; troubleshooting and configuration happen less often.

## Product Purpose

An independent, headless service that copies original photos and videos from Immich (read only) to Amazon Photos through a durable SQLite queue. Success means originals arrive byte-for-byte, nothing is uploaded twice, and nothing in the cloud is deleted unless the operator explicitly opts into trash propagation.

## Operating Context

- Runs as a single container; the dashboard shares the health/readiness listener and is meant for localhost or a trusted private network.
- Server-rendered Go `html/template` pages with a CSRF-protected form per action; no client JavaScript. Counts update on page refresh.
- The CLI (`status`, `doctor`, `report`) exposes the same state for headless use.

## Capabilities and Constraints

- Per-account controls: pause/resume, retry eligible errors, read-only diagnosis, workflow run logs, edit, remove.
- Detailed counters (albums, events, favorites, Live Photo pairs, observed storage, direct reads, filters) exist but are secondary to the glance check and stay collapsed by default.
- Secrets are never shown; only mounted file paths.
- No front-end build step or runtime dependency may be added for the dashboard.

## Evidence on Hand

No real usage screenshots or user quotes; README screenshots use sample data.

## Product Principles

- Honest state over reassurance: show uncertain or failed work plainly.
- Non-destructive by default; destructive controls are explicit and separated.
- Glance first, detail on demand.
- One container, no extra moving parts.
