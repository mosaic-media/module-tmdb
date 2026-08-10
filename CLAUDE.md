# Claude Instructions — module-tmdb

Mosaic's first-party TMDB metadata module: a client of one service, filling the
metadata, search, catalog and settings-UI roles.

It is a **core module** under the guarantee clause of
[architecture#3](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0003-two-module-tiers.md) —
compiled into the Platform binary, first-party, one of a small closed set.

## Being core changes the delivery, not the shape

This is the thing most easily got wrong here. The tier is a **delivery and
coupling decision, not a contract decision.** This module is built exactly as a
third party's is: its own Go module, importing only the published contracts,
invoked through the capability registry. It does not know which tier it is in,
and moving it between tiers would be a build change rather than a rewrite.

So the boundary discipline is *stricter* here, not looser:

- **Import only [`sdk`](https://github.com/mosaic-media/sdk),
  [`contracts`](https://github.com/mosaic-media/contracts) and the standard
  library.** `boundary_test.go` parses every import in the package and fails on
  anything else. The `contracts` exemption is there because this module authors
  its own settings screen
  ([sdk#4](https://github.com/mosaic-media/sdk/blob/main/docs/adr/0004-module-contributed-settings-ui.md),
  [contracts#3](https://github.com/mosaic-media/contracts/blob/main/docs/adr/0003-sdui-contract-repository.md)).
- **No third-party dependency, ever.** A core module shares one dependency graph
  and one address space with the Platform and every other core module. That
  problem is not solved for core modules; it is moved to Mosaic's CI, where it is
  tractable only because the set is small and its members bring nothing with
  them. Read `go.mod` for what that currently costs — the indirect requires are
  the honest measure, and they are not all this module's doing.
- **This module is an anti-corruption layer**
  ([module-stremio-addons#2](https://github.com/mosaic-media/module-stremio-addons/blob/main/docs/adr/0002-modules-as-anti-corruption-layers.md)).
  Every TMDB-ism stops in `tmdb.go`: the two auth schemes, the `title`/`name` and
  `release_date`/`first_air_date` field pairs, image *paths* that are not URLs,
  the per-season episode endpoint, `append_to_response`. Nothing above that file
  sees a TMDB shape, and the Platform must never learn one.

## Do not let it grow into a source

It **must not acquire a stream or subtitle role.** TMDB describes content; it
does not host or index it. A meta-only import — Works and trees with no Parts —
is the correct and complete outcome, not a degraded one.

**Watch providers are the sharpest edge of that rule.** `ContentMetadata.Watch`
names services a title streams on, and it is the one field in this module that
looks like a source and is not. It must never become a `Part`, never become a
source binding, and never be rendered as a play control — Mosaic cannot play a
Netflix listing, and an affordance that suggests otherwise is a promise the
Platform cannot keep. `TestWatchProvidersNeverBecomeParts` and
`TestImportStoresNoAttributesWithoutAvailability` are what hold it; keep them.

## Two things in here are security-relevant

- **A custom catalog's discover query is free text from a settings screen**, and
  it is appended to a request carrying the credential. `sanitiseDiscoverQuery`
  drops everything in `reservedDiscoverParams`; without it a query reading
  `api_key=…` would silently replace the module's own, because the substitution
  happens in `url.Values` before the request is built. **Add to that map, never
  remove from it.**
- **`Certification` is region-exact or empty.** Never fall back to another
  country's rating. Empty means unknown, and a consumer must not read it as
  permissive. **Watch providers follow the same rule** for the same reason:
  availability is national, and a substitute is a wrong answer rather than a
  partial one.
- **The watch data is JustWatch's, and TMDB's terms require crediting them**
  wherever it is shown. The credit travels in `WatchAvailability.Attribution`
  rather than living in a screen, so do not drop it when mapping.

## The bundled token

`defaultReadAccessToken` is linked in at build time with `-ldflags -X`. The six
rules governing every Mosaic project credential are in
[architecture#4](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0004-project-credentials-in-official-builds.md);
these are the three that are easiest to break from inside this repository:

- **`resolveToken` is the only function that reads the symbol.** Anything needing
  to know only *whether* a bundled token exists asks `bundledTokenPresent`, which
  asks `resolveToken` rather than the variable. The two test files that read it
  directly are the deliberate exceptions and ship in no binary. This has been
  broken once already, in the way that is hardest to see: the settings screen's
  presence checks read the variable while the doc comment above `resolveToken`
  still claimed a single reader. **Changing the screen is when to re-check it.**
- **`settings.APIKey` only ever holds the user's own key.** Never populate it
  from the bundled token as a convenience. `configureModule` replaces the whole
  settings document, so the bundled token reaching that field would write a
  shared build-time credential into a user's stored settings the next time they
  touched any control.
- **The settings screen describes the bundled token, never shows it.** There is
  nothing for a user to copy, verify or fix, so any rendered fragment is noise at
  best.

**The `linkercheck` gate is not optional.** `-X` against a symbol that no longer
resolves is *silently ignored*, so a rename would ship a tokenless binary with no
error anywhere. `linkercheck_test.go` links a canary through the same symbol path
a release build uses. Renaming the variable or moving its package means updating
that path in **three** places — `docker-compose.test.yml`, `.github/workflows/verify.yml`
and the release workflow that links the real token.

> **A citation in this repository's own Go comments is wrong and has not been
> corrected here.** `capability.go` attributes the single-reader and three-state
> rules to [supervisor#1](https://github.com/mosaic-media/supervisor/blob/main/docs/adr/0001-supervisor-as-host-manager.md),
> which is the Supervisor's host-manager record and carries no numbered rules at
> all. The record meant is
> [architecture#4](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0004-project-credentials-in-official-builds.md).
> It resolves, so no lint catches it — that is the failure mode the qualified
> citation form exists to make visible, landing here instead.

## Modules are the forcing function for the SDK

When something cannot be expressed, that is a **finding**, not an obstacle to
work around. Take it to the SDK as an additive `v0.x` bump, or record it as an
open gap. **Do not simulate the missing surface locally.**

**What a finding may ask the SDK for is a shape, never an implementation**
([sdk#10](https://github.com/mosaic-media/sdk/blob/main/docs/adr/0010-the-sdk-carries-no-implementation.md)
— read its Status before repeating what it decided as though it were built). A
gap closed by a type or a verb is an SDK bump; a gap that can only be closed by
naming a library belongs behind a declarative Platform surface instead.

**The open gaps live in `README.md`'s "The honest limits", not here.** That is
the published list, it is the one a reader without this file sees, and a second
copy in this file is how the first goes stale. Add to it in the same change that
finds the gap.

## Everything runs in the container, nothing runs on the host

**Do not run `go build`, `go test`, `go vet` or `gofmt` directly on this
machine.**

```bash
docker compose -f docker-compose.test.yml run --rm test
```

That runs gofmt, `go build ./...`, `go vet ./...`, `go test ./...` and the
tagged `linkercheck` pass, against the Go version pinned in the compose file,
which must stay equal to `go.mod`'s. Append `bash` for a shell in the same
environment.

**`.github/workflows/verify.yml` mirrors that compose file step for step** —
same gates, same linker canary — and the two must stay in step: the compose file
is what you run, the workflow is what refuses the push. It uses `setup-go`
rather than nesting a container in a runner, because a fresh runner is already
the clean machine the container exists to simulate.

What the container is protecting is **the boundary**. A host with a populated
module cache, a leftover `go.work` or a stray `replace` can satisfy an import a
third party's machine could not, and `boundary_test.go` still passes because the
import resolved.

The tests are **hermetic** — a fake TMDB over `httptest` reached by rewriting the
request host through the injected `http.Client`, plus an in-memory
`ContentService`. Keep them that way. There is no TMDB key CI could hold that is
not somebody's, and the API base URL is a constant on purpose: adding a settable
field so tests could point elsewhere would put a seam in the production type that
only tests use.

## Versioning and release

A change is a **minor bump**, tagged and pushed. Consumers then move their own
`require`; **a `replace` must never land in a commit.**

```bash
git tag v0.6.0 && git push origin main && git push origin v0.6.0
```

Pushing the tag is the whole publish — there is no artifact, and the module proxy
pulls from the tag on first request. `.github/workflows/release.yml` is what
makes that safe: it reuses `verify.yml` rather than carrying a second copy of the
gates, checks the tag is a semver version and that `go.mod`'s module path matches
the repository, then **proves a consumer can resolve it** by doing `go get` from
a throwaway module through the public proxy. That last step is there because the
proxy and checksum database are eventually consistent with a just-pushed tag, and
without it a bad publish surfaces as a broken build in a consumer repository with
nothing pointing back here.

**`TMDB_RAC` does not belong in this repository's secrets.** `-ldflags -X`
applies when a *binary* is linked and this repository never links one; the secret
belongs to whichever repository links the binary that ships it — which for a core
module is the Platform's release workflow
([architecture#4](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0004-project-credentials-in-official-builds.md)
rule 2).

The module reports the version that was **actually linked**, via
`v1.ModuleVersion` reading the build graph — not a hand-maintained constant,
which nothing forces to agree with anything.

## Workflow

- Observability goes through the SDK's ambient `v1.Telemetry`
  ([sdk#5](https://github.com/mosaic-media/sdk/blob/main/docs/adr/0005-modules-observe-through-the-sdk.md)),
  reached as `TelemetryFrom(ctx)`. Do not print, and do not configure an
  exporter, a sink or retention — the Platform owns the observability plane. The
  API key is a credential: classify it, never write it verbatim.
- **MIT-licensed**
  ([architecture#1](https://github.com/mosaic-media/architecture/blob/main/docs/adr/0001-licensing.md)).
  Files here carry no SPDX header — match the files already present.
- **This repository owns no decision records**, and that is correct rather than
  an oversight: every decision governing it is enforced somewhere else. Do not
  create a `docs/adr/` here to hold one; take it to the repository whose gate,
  composition root or release workflow would enforce it.

<!-- shared-rules:begin -->
## Rules every Mosaic repository shares

*Generated. The source is `architecture/shared/repository-rules.md`; edit it there
and run `scripts/shared_rules.py --write` across the fleet. A copy edited in place
fails its repository's gate, which is the point: these rules were eleven
hand-kept copies in four variants, and the abridged ones had quietly dropped the
reasoning while keeping the rules — and in one case dropped a rule outright.*

### What this file may say

**A `CLAUDE.md` states rules, and facts about its own repository. It does not
state facts about another one — it links instead.**

An audit of all twelve of these files against their source found 74 stale claims.
None of roughly 180 rules was wrong; 62 of the 74 were facts about somebody
else's repository. Ownership predicts rot: a fact about this repository stays true
because whoever changes the code changes the sentence in the same session, and a
fact about another one dies the moment they edit it with nothing here going red.

The same applies to facts this repository already publishes in a generated
artefact — counts, versions, what is built. Point at the artefact.

### Decision records live with the code they govern

Each repository owns the records whose *mechanism* it holds — the spec file, the
lint gate, the conformance corpus, the composition root, the release workflow.
A decision can bind five repositories and still have exactly one steward.

- **`docs/adr/`**, numbered from 1 in every repository, with `docs/adr/README.md`
  a **generated** index. Read the index first; it is the bounded thing.
- **A record's heading carries no number.** The number lives in the filename and
  the index only, so a record's anchor survives being renumbered.
- **Cite a record as `repo#N`, and make it a link** — a relative path within a
  repository, an absolute URL across them, and the bare label only where no URL
  is possible, such as a code comment or a Dockerfile. The old `ADR NNNN`
  spelling is refused by a lint: once every repository numbers from 1, that form
  resolves quietly to a *different* record instead of dangling, and no tool in
  the fleet could detect it.
- **Cross-cutting records stay in [`architecture`](https://github.com/mosaic-media/architecture)** —
  the ones with no enforcing mechanism anywhere: licensing, repository naming and
  topology, the module tier model.

### Decision records are append-only

An ADR is an account of what was decided and why, at a time. It is evidence, not
documentation, and its value is that it was not edited afterwards.

- **Never rewrite a record's body** — not to correct it, not to annotate it, not
  to add "as built, this differs". That turns a record into a running commentary
  and destroys the thing it is for.
- **State changes go in the `**Status:**` line and nowhere else** — built, built
  in part (naming the part), or superseded, wholly or partly.
- **A changed decision earns a new record that supersedes it**, with its own
  Context / Decision / Alternatives / Consequences, and both records then point
  at each other through their Status lines. The old body stays exactly as it was.
- **An unbuilt decision is not a superseded one.** "Not done yet" belongs in the
  Status line and the roadmap; only a reversal earns a new record.

### The roadmap is maintained, not consulted

**`docs/roadmap.md` in [`architecture`](https://github.com/mosaic-media/architecture)
is the single record of where the build is, across every repository.** It stays
there because a milestone spans repositories by construction. Read it before
starting, and **update it in the same session as the change that dates it** — not
in a follow-up, which does not happen.

- A slice that lands is marked landed, **with what it left out named in the same
  sentence**. "Built" with no qualifier claims the whole slice shipped.
- Implementation that departed from its record is recorded where it departed.
  The surprises are the most valuable thing in it.
- **Do not restate the roadmap here.** A second copy of "what is built" in a
  `CLAUDE.md` is how the first copy goes stale unnoticed.
- A capability with no client path is not done — it is
  [owed](https://github.com/mosaic-media/architecture/blob/main/docs/unreachable-capability.md).

### Demonstrated, not asserted

**Say what you actually ran.** A skipped test is not a passed test, and "it should
work" is not evidence.

Each repository's container is the authority on its own gate, and the command is
in that repository's section below. It exists because the checks that matter fail
*soft*: a missing PostgreSQL skips storage tests and still prints `ok`, a missing
generator toolchain produces a drift guard that passes by not running. Where the
container cannot be run, running what you can on the host is better than running
nothing — **provided you report which checks ran and which did not.** Claiming a
gate passed when it was not executed is the one thing this rule exists to stop.

### Commit and push

- **Commit and push each repository separately.** They are siblings on disk and
  independent in git.
- **Commit author identity** must be `AdamNi-7080 <anicholls41@gmail.com>`. If git
  has no identity configured, set it repo-locally rather than globally.
- **Push once the change has been demonstrated working in this session.** Commit
  locally and say so otherwise. **Force-push always requires asking.**
<!-- shared-rules:end -->
