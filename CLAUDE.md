# Claude Instructions — module-tmdb

A client of one upstream service, TMDB. It has no `cmd/` and links no binary of
its own: its release dispatches `core-module-release` at
[`platform`](https://github.com/mosaic-media/platform), which compiles it in.

Fleet-wide conventions — commits, decision records, citation form, the roadmap —
are in [`architecture`](https://github.com/mosaic-media/architecture/blob/main/CLAUDE.md).
This file is what is specific to module-tmdb.

## The boundary, and what the tests hold

- **Import only [`sdk`](https://github.com/mosaic-media/sdk),
  [`contracts`](https://github.com/mosaic-media/contracts) and the standard
  library.** `boundary_test.go` parses the imports of every non-test `.go` file
  and errors on anything else, reporting a `github.com/mosaic-media/platform/`
  import separately from a third-party one. It **reads this directory only**
  (`os.ReadDir(".")`, directories skipped), so adding a subdirectory means making
  that test walk or the boundary goes unchecked inside it.
- **No third-party dependency, ever**, and being compiled in is the reason: a
  dependency added here is one the Platform and every other core module must
  resolve compatibly, in one graph. `go.mod`'s indirect requires are the honest
  measure of what that costs, and they are not all this module's doing.
- `capability_test.go`'s `TestManifestDeclaresOnlyRolesItImplements` asserts
  every declared role has a provider interface behind it, and fails on
  `RoleStream` or `RoleSubtitles`.
- `Manifest` declares `RoleMetadata`, `RoleSearch`, `RoleCatalog` and
  `RoleSettingsUI`. TMDB describes content and does not host or index it, so an
  import creates Works and their season/episode trees with no Parts — the shape,
  not a gap.

## Do not let it grow into a source

- **Watch providers are the sharp edge.** `WatchAvailability` names services a
  title streams on elsewhere; it must never become a Part, a source binding or a
  play control. `TestWatchProvidersNeverBecomeParts` holds the first two. The
  JustWatch credit TMDB's terms require travels in
  `WatchAvailability.Attribution` rather than in a screen, so do not drop it when
  mapping.
- **`Certification` and watch availability are region-exact or empty.** Never
  fall back to another country's rating or another country's availability: empty
  means unknown, and a substitute is a wrong answer rather than a partial one.
- **A custom catalog's discover query is free text from a settings screen**, and
  it is appended to a request carrying the credential. `sanitiseDiscoverQuery`
  drops every name in `reservedDiscoverParams`; without it `api_key=…` would
  replace the module's own credential silently, the substitution happening in
  `url.Values` before the request is built. **Add to that map, never remove from
  it.**

## The bundled read access token

- `defaultReadAccessToken` (`capability.go`) is empty in an ordinary build and
  linked with `-ldflags -X github.com/mosaic-media/module-tmdb.defaultReadAccessToken=…`
  when the **Platform** binary is linked. **`TMDB_RAC` does not belong in this
  repository's secrets** — `-X` applies where a binary is linked and this
  repository links none; `release.yml` says the same.
- **`resolveToken` is the only function that reads the variable.**
  `bundledTokenPresent` asks `resolveToken` rather than the variable, and the two
  test readers — `linkercheck_test.go` and `client_internal_test.go`'s
  `withBundledToken` — ship in no binary. **Changing the settings screen is when
  to re-check that it still asks through the function.**
- **`settings.APIKey` only ever holds the user's own key.** `configureModule`
  replaces the whole settings document, so a bundled token that reached that
  field would be written into a user's stored settings by the next control they
  touched.
- The screen **describes** the bundled token and never renders it. A user's own
  key appears only through `maskKey`, as its last four characters.
- **The linker guard is not optional.** `-X` against a symbol path that no longer
  resolves is *silently ignored*: a rename, a package move or a mistyped module
  path links nothing, the build stays green, and the binary ships with no token —
  surfacing at a user's install rather than in any gate. `linkercheck_test.go`
  (build tag `linkercheck`) links a canary through the same path and asserts
  `resolveToken` uses it. Renaming the variable or moving its package means
  updating that path in `docker-compose.test.yml`, `.github/workflows/verify.yml`
  and the Platform's release build together.

## `tmdb.go` is the anti-corruption layer

Every TMDB-ism stops there and nothing above it sees a TMDB shape. The decode
decisions that look odd and are deliberate:

- **Two credentials for the same endpoints.** A v3 key goes in the `api_key`
  query parameter, a v4 read access token in an `Authorization: Bearer` header;
  `isBearerToken` decides from the shape (a JWT's two dots) rather than asking
  the user which they pasted.
- **One logical record is spread across `append_to_response` sub-objects.**
  `appendFor` differs by type — a film's age rating is in `release_dates`, a
  series' in `content_ratings`. `watch/providers` carries a slash in both the
  sub-request name and the result key; that is TMDB's spelling, not a typo to
  normalise.
- **Keywords arrive under two names** — `keywords` on a film, `results` on a
  series — and `rawTitle.Keywords` decodes both, because decoding one is a silent
  empty list. `created_by` is a top-level field, not a `credits.crew` job.
- **`watch/providers.results` decodes into a map** keyed by ISO 3166-1 country
  code, since TMDB returns every region it knows.
- **Image fields are paths, not URLs.** `images.go` resolves the CDN base and a
  per-surface size from `/configuration`, cached with a built-in fallback, so a
  failed configuration call degrades rather than stopping posters rendering.
- **A series costs one request per season** — TMDB has no whole-show episode
  endpoint — bounded by `seasonFetchConcurrency`.

## The gate

**Do not run `go build`, `go test`, `go vet` or `gofmt` on this machine.**

```bash
docker compose -f docker-compose.test.yml run --rm test
```

That runs `scripts/adr_lint.py`, gofmt, `go build`, `go vet`, `go test` and the
tagged `linkercheck` canary pass, against the Go version pinned in the compose
file — keep it equal to `go.mod`'s. Append `bash` for a shell in the same
environment. `.github/workflows/verify.yml` runs the same checks on a `setup-go`
runner and is what refuses a push, so **keep the two in step.**

The tests are **hermetic** — a fake TMDB over `httptest`, reached by rewriting
the request host through the injected `http.Client`, plus an in-memory
`ContentService`. Keep them that way: no TMDB key CI could hold is not
somebody's, and `apiBase` stays a constant so no seam exists for tests alone.

## Records, release, licence

- **This repository owns no decision records and has no `docs/adr/`.** Take a new
  one to the repository whose gate, composition root or release workflow would
  hold its mechanism.
- `scripts/adr_lint.py` is **vendored** from
  [`architecture`](https://github.com/mosaic-media/architecture) and run by the
  gate. Do not edit it here — change it there and re-vendor.
- **Something the contract cannot express is a finding, not an obstacle to work
  around**, and never a surface simulated locally. The open ones live in
  `README.md`'s "The honest limits" — the published list a reader without this
  file sees. Add to it in the same change that finds the gap; do not keep a
  second copy here.
- Pushing a tag is the whole publish. `release.yml` reuses `verify.yml`, checks
  the tag shape and the module path, creates the release, proves a consumer can
  resolve the version through the public proxy, and only then dispatches; a
  missing `PLATFORM_DISPATCH_TOKEN` fails the run rather than warning.
- **A `replace` must never land in a commit.** The version reported is the one
  actually linked, via `v1.ModuleVersion(modulePath)`; do not replace that with a
  constant.
- **MIT.** Files here carry no SPDX header — match the files already present.
