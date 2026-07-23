# module-tmdb

Mosaic's first-party **metadata** module — a client of [The Movie Database](https://www.themoviedb.org)'s v3 API, built against the [Mosaic SDK](https://github.com/mosaic-media/sdk).

It is a **core module** ([ADR 0062](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0062-two-module-tiers.md)) under the *guarantee* clause: Mosaic cannot function without a metadata/search provider ([ADR 0035](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0035-metadata-as-required-capability.md)), so one must be present in every binary with no install step that can fail. The tier is a delivery decision, not a contract decision — this module is shaped exactly like an extension module, its own Go repository importing only the published contracts, and it does not know which tier it is in.

## Why it exists

Until now Mosaic's guaranteed metadata was a **Stremio addon bundled inside `module-stremio-addons`** — Cinemeta, prepended to the user's addon list and opted out with a `disableDefaultAddons` setting. ADR 0035 recorded that as unresolved ("whether the default belongs to the Platform or to the module is a question this record answers one way and the code answers the other"), and ADR 0062 answered it: a metadata provider Mosaic *guarantees* cannot live inside a module a deployment might not install.

It also closes two gaps that were recorded rather than invented ([ADR 0034](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0034-rich-metadata-preview.md)) because the Stremio addon protocol structurally cannot carry them:

- **Clearlogos.** A detail hero renders its title as a logo image. TMDB has a per-language `images` collection; an addon has one `logo` string that most sources leave empty.
- **Cast with character names and headshots.** A cast *rail* needs faces. Cinemeta returns names.

## What it provides

| Role | What it does |
|---|---|
| `RoleMetadata` | Full detail for a ref — overview, genres, rating, runtime, poster/backdrop/**clearlogo**, billed cast with characters and headshots, and for a series a per-episode preview with stills. |
| `RoleSearch` | Free-text search over film and television. It ships with metadata rather than as an extra: nothing else can produce a ref this module's metadata role would answer for, and ADR 0035 makes the two one required capability class. |
| `RoleCatalog` | Trending, popular, top-rated and in-cinemas/on-air — so a fresh install has rails to render. |
| `RoleSettingsUI` | The API key form ([ADR 0038](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0038-module-contributed-settings-ui.md)). |

It fills **no** stream or subtitle role. TMDB describes content; it does not host or index it. A TMDB-only deployment materialises Works and their season/episode trees with **no Parts** — the meta-only shape the Platform already supports, and the reason metadata and streams are independent concerns.

## Settings

User-managed opaque JSON ([ADR 0021](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0021-module-settings.md)), set through the module's own settings screen:

```json
{
  "apiKey": "…",
  "language": "en-US",
  "region": "GB",
  "includeAdult": false
}
```

`apiKey` accepts **either** a v3 API key or a v4 read access token — a user copying from their TMDB account page has no reason to know which one Mosaic wants, so the credential's shape decides where it is sent.

## The honest limits

Four things this module does not do, recorded rather than papered over.

**It is not zero-configuration, and the guarantee clause assumes zero-configuration.** TMDB has no anonymous access. A binary with this module compiled in satisfies the composition-time check that a `RoleMetadata` and a `RoleSearch` provider are *registered*, but a user still sees nothing until they paste a key. ADR 0035's requirement is "metadata and search work on first boot with zero configuration"; **this module meets the letter and not the spirit**, and no TMDB-based module can. Closing it properly needs either a Mosaic-held key (a distribution and cost question, not a code one) or onboarding that collects the key before the Platform is servable — which is where [ADR 0063](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0063-platform-binary-built-by-ci.md) puts module settings anyway.

**Search results do not dedup against IMDb-keyed sources.** A ref carries one external identity, and a search result's is `tmdb/<id>` — TMDB's search endpoint does not return IMDb ids and fetching them would be a round trip per result. So a TMDB search result for a film already added through a Stremio addon shows as *new* rather than *In library*. The reverse direction **does** work: an import through this module binds the Work under `tmdb` **and** `imdb` when TMDB reports one, so a Stremio search finds it and a re-import through either module is idempotent.

**Collections and "similar" are fetched but have nowhere to go.** TMDB carries `belongs_to_collection`, which is exactly the franchise data ADR 0034 wanted, and `ContentMetadata` has no field for it. Growing the SDK is the next step and is deliberately not taken here alongside a first implementation.

**Changing any setting echoes the API key through the client.** `configureModule` replaces the settings document wholesale — ADR 0021 has no partial update — so a control that sets a language must carry the key or erase it. The key therefore appears inside this screen's action payloads, reaching only an admin who passed `module.configure` but bypassing the Platform's redaction classes ([ADR 0056](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0056-redaction-classes-are-the-pii-boundary.md)), which cannot see inside an opaque module document. It is never *rendered* — the screen shows the last four characters. The fix is an SDK change: a merge semantic on `configureModule`, or a write-only settings field.

## Build and test

**Everything runs in a container; nothing is built or tested on the host.**

```bash
docker compose -f docker-compose.test.yml run --rm test
```

That runs gofmt, `go build`, `go vet` and `go test` against a pinned toolchain. The tests are hermetic — a fake TMDB over `httptest` and an in-memory `ContentService` — so unlike the Stremio module's container this one needs no egress. TMDB requires a key, and there is no key a CI run could hold that is not somebody's.

## Attribution

This product uses the TMDB API but is not endorsed or certified by TMDB. Attribution is a condition of TMDB's API terms, which is why it is rendered in the settings screen rather than only stated here.

## License

MIT — the author's choice, permitted for any Module by the Platform's linking exception ([ADR 0022](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0022-licensing.md)).
