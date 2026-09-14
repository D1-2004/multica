# Product feature releases

The feature-updates surface is a global, authenticated release feed shown inside every workspace shell. It separates stable feature identity from immutable publication history:

- `product_feature` owns the long-lived feature identity and slug.
- `product_feature_release` owns one published new-feature announcement or improvement.
- An improvement always inserts a new release and records `previous_release_id`; published rows are never edited through the API.
- The list orders releases by `published_at DESC, id DESC`. Releases dated in the future remain hidden until their publication time.

The model deliberately does not use database foreign keys. The publishing transaction locks the feature row, resolves the latest release, validates the release type and publication time, and writes the history link atomically.

## Read API

Both read endpoints require normal user authentication but do not require workspace headers because the content is global.

### List and search

```http
GET /api/features?q=agent%20event&limit=30&offset=0
```

Search is case-insensitive. Whitespace-separated terms use AND semantics and match title, description, use cases, usage guide, version label, and required image version. The response is:

```json
{
  "releases": [],
  "total": 0,
  "limit": 30,
  "offset": 0
}
```

### Detail

```http
GET /api/features/{releaseId}
```

Detail includes the complete release content, image-upgrade requirement, and a compact `previous_release` object when history exists.

## Publishing API

```http
POST /api/internal/features/releases
Authorization: Bearer ${MULTICA_LOG_TAIL_TOKEN}
Content-Type: application/json
```

The endpoint uses the existing operator token. When the token is not configured, only a direct loopback request without forwarding headers is accepted for local development.

New feature example:

```json
{
  "feature_slug": "agent-events",
  "release_type": "new",
  "title": "Agent events",
  "description": "Subscribe agents to product events.",
  "use_cases": "Automate work when a matching event arrives.",
  "usage_guide": "Open Events and create a subscription.",
  "version_label": "2026.09.14",
  "image_requirement": "none"
}
```

Improvement example:

```json
{
  "feature_slug": "agent-events",
  "release_type": "improvement",
  "title": "Agent event filters",
  "description": "Filter subscriptions by source and event type.",
  "use_cases": "Reduce irrelevant triggers.",
  "usage_guide": "Edit a subscription and add filters.",
  "version_label": "2026.09.21",
  "image_requirement": "min_version",
  "required_image_version": "multica-runtime:2026.09.21"
}
```

`image_requirement` accepts:

- `none`: no image upgrade; `required_image_version` must be absent.
- `latest_at_publish`: upgrade to the latest image available at publication; a concrete `required_image_version` is still required for auditability.
- `min_version`: upgrade to at least the concrete `required_image_version`.

`published_at` is optional and defaults to the current UTC time. For one feature, every appended release must have a later `published_at` than its predecessor.

## History

- 2026-09-14: Added the stable-feature plus immutable-release protocol, keyword search, previous-version linkage, and concrete image requirements. The split preserves public history while allowing every improvement to appear as a new time-ordered entry.
