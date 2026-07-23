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
| `RoleMetadata` | Full detail for a ref — overview, genres, keywords, age certification, rating, runtime, poster/backdrop/**clearlogo**, billed cast with characters and headshots, trailers, related titles, the franchise a film belongs to, **where it can be streamed, rented or bought**, and for a series a per-episode preview with stills. |
| `RoleSearch` | Free-text search over film and television, plus a reverse lookup from an IMDb id. It ships with metadata rather than as an extra: nothing else can produce a ref this module's metadata role would answer for, and ADR 0035 makes the two one required capability class. |
| `RoleCatalog` | Trending, popular, top-rated and in-cinemas/on-air, **plus any `/discover` query the user defines** — "French thrillers, rated above seven" becomes a browsable catalog like any other. |
| `RoleSettingsUI` | The API key form, locale, and the custom-catalog editor ([ADR 0038](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0038-module-contributed-settings-ui.md)). |

### It answers for other modules' refs

A ref whose native id is an IMDb id — which is every ref from Cinemeta or a Stremio addon, and under [ADR 0072](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0072-the-guaranteed-metadata-provider-needs-no-credential.md) an IMDb-keyed source is the guaranteed floor every deployment has — is resolved through TMDB's `/find` before anything else happens. Without it the richer provider could not describe a single work in such a library, because it would hold no identifier it recognised.

It does **not** decide that TMDB *should* answer for another module's ref. Which provider wins is the open precedence seam ADR 0035 named, and it belongs to the Platform. This only makes the module capable of answering when asked.

### Watch providers

A detail carries where the title can be watched **outside Mosaic** — the services that stream it on subscription, or rent or sell it. Three things about that are load-bearing:

- **An offer is not a source.** It never becomes a `Part`, it is never bound as a source binding, and nothing in it is playable through the Platform. A client that renders an offer as a play control is making a promise the Platform cannot keep; the right affordance is informational, and `Link` is where it should send the viewer.
- **It is region-exact, or absent.** Availability differs entirely by country. With no `region` set the module returns nothing rather than picking one of the hundred-odd TMDB reports — telling a viewer in Britain that something is on a service carrying it only in the US is worse than telling them nothing. Empty offers *with* a link is a real answer meaning "none known here", which is not the same as no data.
- **It carries its attribution.** TMDB's availability data is compiled by **JustWatch**, and TMDB's terms require crediting them wherever it is shown. The credit travels in the value rather than being something a screen has to remember, so any consumer that renders offers has it to hand.

A service listed under several terms — rent *and* buy is the norm — appears once, under the best terms available, because the groups are read subscription-first.

**Availability is also written to the node at import**, under the `tmdbWatch` attribute key, so the library can be *grouped* by service. Everything else descriptive is re-derived live from the provider (ADR 0034); this is stored for the same reason artwork is (ADR 0071) — a question asked across the whole library cannot be answered by a round trip per title. The stored shape is:

```json
{"tmdbWatch": {
  "version": 1,
  "region": "GB",
  "checkedAt": "2026-07-23T21:14:00Z",
  "providers": ["Prime Video", "Netflix", "Apple TV"],
  "offers": [{"provider": "Prime Video", "type": "subscription"}]
}}
```

The flat `providers` array duplicates `offers` on purpose: containment against an array of strings is what an index answers efficiently, so `SearchContentQuery.AttributesContain` with `{"tmdbWatch":{"providers":["Netflix"]}}` is the query. `WatchAttribute` and `WatchAttributeVersion` are **exported constants**, because the moment anything queries that key it is a published shape rather than a private one.

**Nothing refreshes it.** `checkedAt` exists so a consumer can say how old the answer is and so a future scheduled refresh knows what to re-fetch first — see the limits below.

### The endpoints it uses

`/search/multi`, `/find/{imdb_id}`, `/movie/{id}` and `/tv/{id}` (with `credits`, `images`, `external_ids`, `keywords`, `recommendations`, `videos`, `watch/providers` and certifications folded in by `append_to_response`), `/tv/{id}/season/{n}`, `/collection/{id}`, `/configuration`, the eight built-in list endpoints, and `/discover/{movie,tv}`.

A **film detail is one request**; a **series detail is one per season**, because TMDB has no endpoint returning a show's whole episode list; a film in a franchise costs one more. There is no cache, so a detail screen re-fetches on every render — the same trade the roadmap already records for the metadata path generally, and a durable metadata cache is the named follow-up.

It fills **no** stream or subtitle role. TMDB describes content; it does not host or index it. A TMDB-only deployment materialises Works and their season/episode trees with **no Parts** — the meta-only shape the Platform already supports, and the reason metadata and streams are independent concerns.

## Settings

User-managed opaque JSON ([ADR 0021](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0021-module-settings.md)), set through the module's own settings screen:

```json
{
  "apiKey": "…",
  "language": "en-US",
  "region": "GB",
  "includeAdult": false,
  "catalogs": [
    {"name": "French Thrillers", "type": "movie", "query": "with_genres=53&with_original_language=fr"}
  ]
}
```

`apiKey` accepts **either** a v3 API key or a v4 read access token — a user copying from their TMDB account page has no reason to know which one Mosaic wants, so the credential's shape decides where it is sent. It is optional: see the bundled token below.

`region` decides three things beyond which release dates apply: it is the country whose **age certification** is reported, the country whose **watch providers** are listed, and in both cases an unset region means **no claim at all** rather than a substitute. A US "R" shown to a household that set `GB` is not a conservative approximation — it is a different scale reported as if it were theirs.

`catalogs` are raw `/discover` parameters, deliberately. A filter builder would model every parameter TMDB has and go stale the moment it added one; a raw query is a power-user surface that reaches all of `/discover`. Queries are **sanitised before use** — `api_key`, `page`, `language` and `include_adult` are stripped, so a query cannot replace the credential the module sends or fight its paging.

## The bundled token

Mosaic's release build links a TMDB **API Read Access Token** into the binary, so a deployment has working metadata before anyone configures anything:

```bash
go build -ldflags "-X github.com/mosaic-media/module-tmdb.defaultReadAccessToken=$TMDB_RAC" ./cmd/mosaic-platform
```

A user's own key always wins — set one and it takes over immediately, no flag and no restart; clear it and the bundled token resumes. `resolveToken` is the only function that reads it, which is what keeps the separation checkable by reading rather than by trust: `settings.APIKey` holds the user's key and nothing else, everywhere.

**It is never rendered, never written into the settings document, and never logged.** That last one matters more than it looks: `configureModule` replaces the whole settings document, so every control on the settings screen carries one — if the bundled token ever reached `settings.APIKey` it would be written into the user's stored settings by the next control they touched, silently turning a shared build-time credential into their configuration. Two tests hold that line.

**It is not a secret once the binary ships.** A string linked into a distributed binary comes back out with `strings`. This is a *shared* credential whose exposure is accepted, not a hidden one. What makes that acceptable is what it can do — read-only, TMDB's public catalogue, centrally revocable. What it costs is a shared rate limit and a single point of failure across every deployment that has not set its own, which is exactly why the override exists and why the settings screen says which key is in use.

**`-X` on a path that does not resolve is silently ignored**, so a rename would ship a binary with no token and no error anywhere. The container gate runs a second pass under the `linkercheck` build tag that links a canary through the same symbol path and fails if it does not arrive.

## The honest limits

Recorded rather than papered over.

**It is not zero-configuration, and the guarantee clause assumes zero-configuration.** TMDB has no anonymous access, so a user sees nothing until they paste a key. ADR 0035's requirement is "metadata and search work on first boot with zero configuration"; **no TMDB-based module can meet that**, which is why [ADR 0072](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0072-the-guaranteed-metadata-provider-needs-no-credential.md) puts a credential-free provider underneath as the floor and leaves this one as the richer option a deployment opts into.

**Search results do not dedup against IMDb-keyed sources.** A ref carries one external identity and a search result's is `tmdb/<id>`; TMDB's search endpoint returns no IMDb id and fetching one would be a round trip per result. So a TMDB search result for a film already added through an IMDb-keyed source shows as *new* rather than *In library*. Both other directions work: an import binds the Work under `tmdb`, `imdb` and (for a series) `tvdb`, and `/find` resolves an incoming IMDb ref to a TMDB id — so a re-import through any of them is idempotent.

**Collections and similar are detail-screen only.** Both now have SDK fields (`v0.17.0`) and both render for a virtual or a library item. What does *not* happen is persisting them: `RelateContent` could write a `RelationCollectionMember` edge, but `ContentService` has **no relation read**, so an edge written today could never be read back. Until `ListFrom`/`ListTo` exist, a franchise is something the provider re-derives rather than something the library knows.

**Artwork candidates are fetched and discarded.** The `images` response carries every poster and backdrop variant; `v1.Artwork` holds one string per slot. ADR 0071 anticipates this — "a future candidate set and user selection grows this value" — but it changes what a stored artwork value *means*, which is an ADR rather than a field.

**Changing any setting echoes the API key through the client.** `configureModule` replaces the settings document wholesale — ADR 0021 has no partial update — so a control that sets a language must carry the key or erase it. The key therefore appears inside this screen's action payloads, reaching only an admin who passed `module.configure` but bypassing the Platform's redaction classes ([ADR 0056](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0056-redaction-classes-are-the-pii-boundary.md)), which cannot see inside an opaque module document. It is never *rendered* — the screen shows the last four characters. The fix is an SDK change: a merge semantic on `configureModule`, or a write-only settings field.

**Grouping the library by provider has no client path, deliberately.** The storage half is here and the query half is `SearchContentQuery.AttributesContain` (SDK `v0.19.0`), so the capability exists — but availability churns monthly and nothing refreshes what was written at import. A group that says "on Netflix" for something that left in March is actively wrong in a way an absent group is not, so the surface waits on the jobs runner, scheduler and system principal. It is recorded in the [unreachable capability](https://github.com/mosaic-media/architecture/blob/main/docs/unreachable-capability.md) register rather than shipped early.

**Deliberately absent.** The person endpoints (a cast chip opening "more from this actor" needs a node kind for a person, which is a design question rather than an endpoint), alternative titles (matters for matching local files; waits on a local-media module), and `/movie/changes` (would drive incremental refresh; waits on the jobs runner, scheduler and system principal).

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
