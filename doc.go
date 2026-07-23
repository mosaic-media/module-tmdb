// Package tmdb is Mosaic's first-party metadata module: a client of The Movie
// Database's v3 HTTP API, filling the metadata, search and catalog provider
// roles (ADR 0027) over film and television.
//
// It is a **core module** (ADR 0062) under the guarantee clause — Mosaic cannot
// function without a metadata/search provider (ADR 0035), so one must be present
// in every binary with no install step that can fail. That is a delivery and
// coupling decision, not a contract decision: this module is shaped exactly like
// an extension module, its own Go repository importing only the published SDK
// and the SDUI contract, and it does not know which tier it is in.
//
// It exists because the metadata Mosaic ships with was, until now, a Stremio
// addon bundled inside module-stremio-addons — a *default belonging to an
// extension module*, which ADR 0035 recorded as unresolved and ADR 0062 answered
// the other way. A metadata provider Mosaic guarantees cannot live inside a
// module a deployment might not install.
//
// # What it provides
//
//   - RoleMetadata — full descriptive detail for a ref: overview, genres,
//     rating, runtime, poster/backdrop/**clearlogo**, billed cast with character
//     names and headshots, and for a series a per-episode preview with stills.
//     Two of those — the logo and the cast photographs — are what a Stremio
//     meta addon structurally cannot supply (ADR 0034's recorded gaps).
//   - RoleSearch — free-text search over film and television, the other half of
//     the capability class ADR 0035 requires. Without it nothing can produce a
//     ref this module's metadata role would answer for, so the two ship together
//     rather than search being an extra.
//   - RoleCatalog — trending, popular, top-rated and in-cinemas/on-air
//     collections, so a fresh install has rails to render rather than an empty
//     home screen.
//   - RoleSettingsUI — the API key form. TMDB has no anonymous access, so the
//     module is inert until a key is set; the screen is the only path to setting
//     one (ADR 0038).
//
// It does **not** fill RoleStream or RoleSubtitles. TMDB describes content and
// does not host or index it, so a TMDB-only deployment materialises Works and
// their season/episode trees with no Parts — the meta-only shape the Platform
// already supports, and the reason metadata and streams are independent
// concerns.
//
// It owns no schema (ADR 0012): everything it writes goes through
// ContentService, acting as the Caller the Platform hands it (ADR 0017).
//
// # Attribution
//
// This product uses the TMDB API but is not endorsed or certified by TMDB.
// Attribution is a condition of TMDB's API terms, which is why it appears in
// the settings screen this module renders rather than only in a README.
package tmdb
