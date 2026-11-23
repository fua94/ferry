# Channels: opt-in separate timelines

This document is the pull request description for the `feature/channels` branch,
kept in the repository so the rationale is versioned next to the code.

Ferry is one shared timeline; that is a large part of why it is this small. A
deployment with a phone, a laptop and a tablet may still need work links
separated from personal ones without running one server per timeline.

Channels are **opt-in and additive**.

## Behaviour

- A request that does not mention a channel behaves exactly as before: it reads
  and writes `default`. The native iOS and Android apps keep working untouched
  and simply see `default`.
- `channels` and `device_channels` tables; `messages.channel_id` and
  `retractions.channel_id` are added through the existing `ALTER TABLE`
  migration path with `DEFAULT 'default'`, so a v1.0.5 database opens unchanged
  and every existing device becomes a member of `default`.
- `POST /api/v1/channels` creates a channel (slug, 1–32 characters, lowercase
  letters, digits and inner hyphens) and joins its creator. An existing slug is
  `409 channel_exists`; an invalid slug is `400`.
- `POST /api/v1/channels/join` adds a device that already holds a token to a
  channel that already exists (`204`, idempotent). Without it, a device could
  only ever see the channels it was created in.
- `GET /api/v1/channels` returns every channel on the server with a `joined`
  flag for the requesting device, because the picker is a sidebar list.
- `GET /api/v1/messages`, `POST /api/v1/messages/text`,
  `POST /api/v1/messages/file`, `GET /api/v1/devices`, `GET /api/v1/files/{id}`
  and `POST /api/v1/files/{id}/link` accept an optional `channel` query
  parameter. `DELETE /api/v1/messages/{id}` resolves the channel from the
  message itself.
- A channel the device is not a member of answers `404`, the same as a channel
  that does not exist, so the API never confirms what exists. `join` is the only
  way to tell, and that is deliberate: it matches the "trusted LAN, no accounts"
  model.
- Any member of a channel can recall any message in it. That keeps Ferry's
  existing no-owner model instead of introducing roles.
- The access password stays server-wide. Per-channel passwords would be a
  permission system, which Ferry deliberately does not have.

## Web app

- The channel picker is a list in the sidebar, not a dropdown: every channel,
  the active one highlighted, the ones not joined dimmed.
- Clicking a channel that is not joined joins it and opens it.
- The connect address and QR code carry the active channel, so a device that
  scans it lands in the timeline it was invited to instead of `default`.

## Tests and verification

- `go build ./...`, `go vet ./...`, `go test -race -count=1 ./...` all pass. New
  tests cover the migration from a v1.0.5 schema, channel isolation, membership
  enforcement, the join endpoint, and the compatibility path with no `channel`
  parameter.
- Beyond the Go tests, the built image was exercised over HTTP: two channels,
  text messages and a file upload, cross-channel `404`s for reads, writes,
  recalls and downloads, per-channel device lists, and a fresh browser joining
  through a `?channel=` link.
- Image size is unchanged at 19.2/19.3 MB.

## Scope note

`docs/product-core.md` lists "group management" as out of scope. Channels are
smaller than that: no accounts, no invitations, no roles, no presence, no
admin. A channel is *visibility*, not permission. A deployment that prefers one
timeline can ignore the feature entirely, because the default path is byte for
byte the old behaviour.
