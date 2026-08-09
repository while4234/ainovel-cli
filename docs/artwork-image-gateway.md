# AI2API Image Gateway

AINovel uses one optional global AI2API gateway for image-model discovery and
project artwork generation. Configure it only in the global
`~/.ainovel/config.json`; project overlays do not override or persist the
gateway credential.

```jsonc
{
  "image_gateway": {
    "base_url": "http://127.0.0.1:8000",
    "api_key": "replace-with-ai2api-key",
    "default_model": "a2e",
    "request_timeout_seconds": 330
  }
}
```

The base URL accepts a gateway root, `/v1`, `/v1/models`, or the full
`/v1/images/generations` endpoint and is stored as one normalized root. URLs
with userinfo, query strings, fragments, non-HTTP schemes, or encoded paths are
rejected. API responses expose only `has_api_key`. On config updates, an
omitted `api_key` preserves the saved value and `clear_api_key: true` removes
it.

## HTTP APIs

- `GET /api/artwork/config` returns the public gateway configuration.
- `PUT /api/artwork/config` atomically saves normalized global configuration.
- `POST /api/artwork/config/verify` performs only an authenticated
  `GET <gateway>/v1/models`. It never calls image generation.
- `GET /api/artwork/models` returns registry version
  `artwork-image-capabilities/v1`, the 12 StarWriter-verified defaults, and the
  three disabled discovery-only entries.

The internal generation client derives `size`, `aspect_ratio`, and `resolution`
only from the selected registry capability. It always submits `n: 1` and
`response_format: "b64_json"`, makes at most one POST, bounds both JSON and
decoded image sizes, and marks a connection interruption before a response as
uncertain delivery. Callers must not retry an uncertain delivery automatically.

For AI-authored artwork drafts, the gateway receives the exact editable plain
text stored in the immutable prompt version. Prompt generation does not add a
second image-side transformation, expansion, or repair call. Source snapshot,
model audit, and stale-confirmation provenance stay in the project artwork
workspace; no text-provider credential is copied into image requests.

The durable project-scoped store, one-shot worker, recovery rules, and gallery
HTTP APIs are documented in [Project Artwork Workspace](artwork-workspace.md).

## Test safety

Automated tests use `httptest` servers or fake transports only. They must never
call a live model, a paid gateway, or any real `/v1/images/generations`
endpoint. Live gateway verification and live image generation are deliberately
outside the automated validation contract.
