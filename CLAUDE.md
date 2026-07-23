# Claude Instructions — module-tmdb

Mosaic's first-party TMDB metadata module. It is a **core module**
([ADR 0062](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0062-two-module-tiers.md))
under the *guarantee* clause — Mosaic is not Mosaic without a way to identify and
find content — which means it is compiled into the Platform binary by Mosaic's
CI, first-party, and part of a small closed set.

## Being core changes the delivery, not the shape

This is the thing most easily got wrong here. The tier is a **delivery and
coupling decision, not a contract decision.** This module is built exactly as a
third party's is: its own Go module, importing only the published SDK and SDUI
contracts, invoked through the capability registry. It does not know which tier
it is in, and moving it between tiers would be a build change rather than a
rewrite.

So the boundary discipline is *stricter* here, not looser:

- **Import only [`sdk`](https://github.com/mosaic-media/sdk),
  [`sdui`](https://github.com/mosaic-media/sdui) and the standard library.**
  `boundary_test.go` parses every import and fails on anything else. `sdui` is
  allowed because this module authors its own settings screen
  ([ADR 0038](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0038-module-contributed-settings-ui.md)).
- **No third-party dependency, ever.** A core module shares one dependency graph
  and one address space with the Platform and every other core module. That
  problem is not solved for core modules; it is moved to Mosaic's CI, where it is
  tractable only because the set is small and its members bring nothing with
  them.
- **This module is an anti-corruption layer**
  ([ADR 0051](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0051-modules-as-anti-corruption-layers.md)).
  Every TMDB-ism stops in `tmdb.go`: the two auth schemes, the `title`/`name` and
  `release_date`/`first_air_date` field pairs, image *paths* that are not URLs,
  the per-season episode endpoint, `append_to_response`. Nothing above that file
  sees a TMDB shape, and the Platform must never learn one.

## Do not let it grow into a source

It fills metadata, search, catalog and settings-UI roles and **must not acquire
a stream or subtitle role.** TMDB describes content; it does not host or index
it. A meta-only import — Works and trees with no Parts — is the correct and
complete outcome, not a degraded one.

## Modules are the forcing function for the SDK

When something cannot be expressed, that is a **finding**, not an obstacle to
work around. Take it to the SDK as an additive `v0.x` bump, or record it in the
roadmap as an open gap. **Do not simulate the missing surface locally.** This
module has already forced one bump — SDK `v0.17.0` added `Keywords`,
`Certification`, `Similar`, `Collection` and `Trailers` because TMDB returns all
five and there was nowhere to put them. Three remain open, all written up in the
README's "honest limits":

- **No relation read.** `RelateContent` can write a `RelationCollectionMember`
  edge and nothing in `ContentService` can read one back, so a franchise is
  re-derived per render rather than known to the library. Do not write edges
  nothing can read.
- **Artwork candidates.** The `images` response carries every poster and backdrop
  variant and `v1.Artwork` holds one string per slot. ADR 0071 anticipates a
  candidate set; changing what a stored artwork value *means* is an ADR, not a
  field.
- `configureModule` **replaces** the settings document; there is no merge. A
  module with a secret setting must therefore echo the secret through the client
  to change any other setting. See the comment on `configureInput`.

## Two things in here are security-relevant

- **A custom catalog's discover query is free text from a settings screen**, and
  it is appended to a request carrying the credential. `sanitiseDiscoverQuery`
  strips `api_key`, `page`, `language` and `include_adult`; without it a query
  reading `api_key=…` would silently replace the module's own. Do not widen that
  list's inverse — add to `reservedDiscoverParams`, never remove.
- **`Certification` is region-exact or empty.** Never fall back to another
  country's rating. Empty means unknown, and a consumer must not read it as
  permissive.

## Everything runs in the container, nothing runs on the host

**Do not run `go build`, `go test`, `go vet` or `gofmt` directly on this
machine.**

```bash
docker compose -f docker-compose.test.yml run --rm test
```

That runs gofmt, `go build ./...`, `go vet ./...` and `go test ./...` against the
Go version pinned in the compose file, which must stay equal to the one in
`go.mod`. Append `bash` for a shell in the same environment.

The tests are **hermetic** — a fake TMDB over `httptest` reached by rewriting the
request host through the injected `http.Client`, plus an in-memory
`ContentService`. Keep them that way. There is no TMDB key CI could hold that is
not somebody's, and the API base URL is a constant on purpose: adding a settable
field so tests could point elsewhere would put a seam in the production type that
only tests use.

## Versioning and release

The Platform requires this at a **tagged version with no `replace`** — a
`replace` must never land in a commit. A change is a minor bump, tagged and
pushed, then the Platform's `go.mod` require is bumped to match.

```bash
git tag v0.2.0 && git push origin main && git push origin v0.2.0
```

The module reports the version that was **actually linked**, via
`v1.ModuleVersion` reading the build graph — not a hand-maintained constant,
which nothing forces to agree with anything.

## Workflow

- Commit and push this repository **separately** from `platform`.
- **Commit author identity** must be `AdamNi-7080 <anicholls41@gmail.com>`.
- The test container green before pushing.
- Observability goes through the SDK's ambient `v1.Telemetry`
  ([ADR 0059](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0059-modules-observe-through-the-sdk.md)),
  reached as `TelemetryFrom(ctx)`. Do not print, and do not configure an
  exporter, a sink or retention — the Platform owns the observability plane.
  The API key is a credential: classify it, never write it verbatim.
- **MIT-licensed**, unlike the Platform's AGPL
  ([ADR 0022](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0022-licensing.md)).
  Files here carry no SPDX header — match the files already present.

## The roadmap and the decision records

These rules are identical in every Mosaic repository. They exist because the
state of the build and the reasons behind it are the two things that rot fastest
and report nothing when they do — no build fails, no test goes red.

### The roadmap is maintained, not consulted

**`docs/roadmap.md` in [`architecture`](https://github.com/mosaic-media/architecture)
is the single record of where the build is.** Read it before starting work, and
**update it in the same session as the change that dates it** — not in a
follow-up, which does not happen.

- **A slice that lands is marked landed, with what was left out.** "Built" with
  no qualifier is a claim that the whole slice shipped; if part of it did not,
  say which part and why in the same sentence.
- **Implementation that departs from the plan is recorded where it departed.**
  The roadmap is derived from the code, not from the intention that preceded it,
  and the surprises are the most valuable thing in it.
- **Do not restate the roadmap here.** A second copy of "what is built" in a
  `CLAUDE.md` is how the first copy goes stale unnoticed.
- **A capability with no client path is not done — it is
  [owed](https://github.com/mosaic-media/architecture/blob/main/docs/unreachable-capability.md).**
  If you delete or fail to build a client path to a working service, add its row
  to that register in the same change.

### Decision records are append-only

An ADR is an account of what was decided and why, at a time. It is evidence, not
documentation, and its value is that it was not edited afterwards.

- **Never rewrite a record's body to match what was built.** Not to correct it,
  not to annotate it, not to add "as built, this differs".
- **State changes in the `**Status:**` line, and nowhere else** — built, built in
  part (naming the part), or superseded, wholly or partly.
- **A changed decision needs a new record that supersedes it**, with its own
  Context / Decision / Alternatives / Consequences. Both records then stand.
- **An unbuilt decision is not a superseded one.** "Not done yet" belongs in the
  Status line and the roadmap.
- **Records live only in `architecture/docs/adr/`**, numbered sequentially in
  kebab-case. Adding one means adding it to `nav:` in `mkdocs.yml`, and
  `mkdocs build --strict` must pass.

**If the code and a record disagree, say so rather than quietly picking one.** An
honest "this is unresolved" is worth more than a plausible reconciliation that
reads as settled.
