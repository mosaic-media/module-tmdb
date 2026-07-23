package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

const (
	// CapabilityID is the id the Platform registers this module under, the id a
	// ref names to route back here, and the key its settings document is stored
	// under (ADR 0021).
	CapabilityID = "tmdb"
	// modulePath is this module's import path, which is how it reads its own
	// version out of the build graph rather than carrying a constant nothing
	// forces to stay true.
	modulePath = "github.com/mosaic-media/module-tmdb"
	// providerScheme is the external-id scheme and source-binding provider this
	// module keys content under. TMDB's own numeric id is the primary key: it is
	// the one every TMDB record has, whereas an IMDb id is present on most and
	// guaranteed on none.
	providerScheme = "tmdb"
	// imdbScheme and tvdbScheme are the *other* schemes a materialised work is
	// bound under when TMDB reports them. They are what let a title added through
	// this module dedup against sources that key on something else — every
	// Stremio addon and Cinemeta use IMDb ids, and television-oriented sources
	// commonly use TVDB — without which the same film added from two sources
	// would be two Works. TMDB reports a TVDB id for series only.
	imdbScheme = "imdb"
	tvdbScheme = "tvdb"
	// wikidataScheme is recorded in the Work's external-id document but not bound
	// as a source: nothing sources content from Wikidata. It is carried because
	// it is the stable identifier across every other database, so a future
	// reconciliation has something to join on and does not have to re-derive it.
	wikidataScheme = "wikidata"
	// defaultLanguage is the language TMDB is queried in when a user has set
	// none. TMDB requires *some* language and falls back to English itself; being
	// explicit means the value shown in settings is the value in use.
	defaultLanguage = "en-US"
)

// errNoAPIKey is returned by every role when the module has no usable
// credential. It is a sentinel rather than a formatted error because it is the
// module's one expected failure — TMDB has no anonymous access, so a fresh
// install hits this until a key is set — and every surface that reports it
// should say the same thing.
var errNoAPIKey = errors.New("TMDB API key not set — add one in Settings › TMDB (the module cannot read anything without it)")

// Capability satisfies the SDK's capability contract and every provider role it
// declares. The assertions fail to compile if the module drifts from what the
// Platform invokes or from a role it claims to fill (ADR 0027).
var (
	_ v1.Capability         = (*Capability)(nil)
	_ v1.MetadataProvider   = (*Capability)(nil)
	_ v1.SearchProvider     = (*Capability)(nil)
	_ v1.CatalogProvider    = (*Capability)(nil)
	_ v1.SettingsUIProvider = (*Capability)(nil)
)

// Capability is the TMDB metadata module. It holds only an HTTP client: the API
// key, language and region it works under are user-managed settings the Platform
// hands in on every invocation (ADR 0021), so one registered module serves
// whatever each deployment configures.
type Capability struct {
	httpClient *http.Client
	// images caches TMDB's published CDN layout across invocations. It is the one
	// piece of state here, and it is on the Capability rather than the Client
	// because a Client is per-invocation and this never changes.
	images imageConfigCache
}

// New builds the capability over an HTTP client (nil for a default). The
// Platform passes its own, which carries the netguard dial guard and the
// outbound telemetry seam; a module that builds its own bypasses both.
func New(httpClient *http.Client) *Capability {
	return &Capability{httpClient: httpClient}
}

// settings is the shape this module reads from its user-managed settings
// document. The Platform stores it uninterpreted; the meaning is entirely here.
type settings struct {
	// APIKey is a TMDB v3 API key or a v4 read access token — either is
	// accepted, since a user copying from TMDB's account page has no reason to
	// know which one Mosaic wants.
	APIKey string `json:"apiKey"`
	// Language is a TMDB language tag ("en-US", "de-DE"). It selects the
	// language of overviews, titles and artwork.
	Language string `json:"language"`
	// Region is an ISO 3166-1 country code affecting release-dated catalogs —
	// what is in cinemas differs by country.
	Region string `json:"region"`
	// IncludeAdult admits adult titles to search results. Off unless set.
	IncludeAdult bool `json:"includeAdult"`
	// Catalogs are the user's own `/discover` queries, each becoming a browsable
	// catalog beside the built-in ones.
	Catalogs []customCatalog `json:"catalogs"`
}

// customCatalog is one user-defined discover query. Query is raw TMDB discover
// parameters — deliberately, since the alternative is a form that models every
// filter TMDB has and goes stale the moment it adds one. It is sanitised before
// use, so a query cannot override the module's credential or paging.
type customCatalog struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Query string `json:"query"`
}

// settingsFrom parses the module's settings document, applying defaults. An
// empty document is valid and means "nothing configured" — which for this module
// means no API key, reported by clientFrom rather than here, so that a settings
// screen can still render for a user who has not set one yet.
func settingsFrom(document []byte) (settings, error) {
	s := settings{Language: defaultLanguage}
	if len(document) == 0 {
		return s, nil
	}
	var parsed settings
	if err := json.Unmarshal(document, &parsed); err != nil {
		return settings{}, fmt.Errorf("parse module settings: %w", err)
	}
	parsed.APIKey = strings.TrimSpace(parsed.APIKey)
	parsed.Language = strings.TrimSpace(parsed.Language)
	parsed.Region = strings.ToUpper(strings.TrimSpace(parsed.Region))
	if parsed.Language == "" {
		parsed.Language = defaultLanguage
	}
	for i := range parsed.Catalogs {
		parsed.Catalogs[i] = normaliseCatalog(parsed.Catalogs[i])
	}
	return parsed, nil
}

// normaliseCatalog splits a catalog entered as one "name | query" string into
// its two parts, and defaults the type.
//
// The settings screen has to submit the pair as a single value — a SubmitField
// submits on its own and there is nowhere to hold a half-finished entry between
// two of them — so this is where the pair comes apart. It is idempotent: once
// split, the next write stores the two fields separately and this does nothing.
func normaliseCatalog(c customCatalog) customCatalog {
	c.Name = strings.TrimSpace(c.Name)
	c.Query = strings.TrimSpace(c.Query)
	if c.Query == "" {
		if name, query, ok := strings.Cut(c.Name, "|"); ok {
			c.Name, c.Query = strings.TrimSpace(name), strings.TrimSpace(query)
		}
	}
	if c.Type != typeTV {
		c.Type = typeMovie
	}
	return c
}

// clientFrom builds a configured client from the settings document, refusing
// when there is no API key. That refusal is the module's whole first-run story:
// the capability is registered and every role is reachable, and each says the
// same actionable thing until a key exists.
//
// It takes a context because resolving TMDB's image CDN layout may need a call.
// That call is cached for a day and falls back to a built-in default on any
// failure, so this stays one request per invocation in the steady state and
// never fails for want of it.
func (c *Capability) clientFrom(ctx context.Context, document []byte) (*Client, error) {
	s, err := settingsFrom(document)
	if err != nil {
		return nil, err
	}
	if s.APIKey == "" {
		return nil, errNoAPIKey
	}
	// Built once with the default so it holds the credential the fetch needs,
	// then again with whatever the fetch resolved. Cheap: a Client is a struct of
	// strings over a shared HTTP client.
	probe := NewClient(c.httpClient, s, defaultImageConfig)
	return NewClient(c.httpClient, s, c.images.get(ctx, probe.fetchImageConfig)), nil
}

// resolveRef translates a ref this module did not produce into TMDB's own ids.
//
// A ref whose NativeID is already a TMDB id passes straight through. One
// carrying an IMDb id — which is every ref from Cinemeta or a Stremio addon, and
// under ADR 0072 that is the guaranteed floor every deployment has — is resolved
// through `/find`. Without this the richer provider could not describe a single
// work in such a library, because it would hold no identifier it recognised.
//
// Note what this does *not* do: it does not decide that TMDB should answer for
// another module's ref. Which provider wins for a given ref is the open
// precedence seam ADR 0035 named, and it is the Platform's to settle. This only
// makes the module capable of answering when asked.
func (c *Client) resolveRef(ctx context.Context, ref v1.ContentRef) (string, string, error) {
	nativeID, nativeType := ref.NativeID, ref.NativeType
	if nativeID == "" {
		return "", "", fmt.Errorf("ref needs a native id")
	}
	if !isIMDbID(nativeID) {
		if nativeType != typeMovie && nativeType != typeTV {
			return "", "", fmt.Errorf("unsupported TMDB type %q; expected %q or %q", nativeType, typeMovie, typeTV)
		}
		return nativeID, nativeType, nil
	}

	found, foundType, ok, err := c.FindByIMDb(ctx, nativeID)
	if err != nil {
		return "", "", fmt.Errorf("resolve IMDb id %s: %w", nativeID, err)
	}
	if !ok {
		return "", "", fmt.Errorf("TMDB has no film or series for IMDb id %s", nativeID)
	}
	return found, foundType, nil
}

// isIMDbID reports whether an identifier is IMDb's rather than TMDB's. IMDb ids
// are "tt" followed by digits; TMDB's are bare integers, so the two are
// unambiguous.
func isIMDbID(id string) bool {
	return strings.HasPrefix(id, "tt") && len(id) > 2
}

// Manifest is the module's self-declaration, including the provider roles it
// fills (ADR 0027). It sources metadata, searches and browses catalogs, and
// contributes its own settings screen. It declares no stream or subtitle role:
// TMDB describes content, it does not host or index it.
func (c *Capability) Manifest() v1.Manifest {
	return v1.Manifest{
		ID:      CapabilityID,
		Version: v1.ModuleVersion(modulePath),
		Name:    "TMDB metadata",
		Provides: []v1.Role{
			v1.RoleMetadata, v1.RoleSearch, v1.RoleCatalog, v1.RoleSettingsUI,
		},
	}
}

// Import materialises the virtual item named by req.Ref — a result a search or
// catalog browse produced (ADR 0028) — into the object graph.
//
// It creates the Work with its artwork and external ids, binds the source, and
// builds the containment tree: a film as Work → feature item, a series as Work →
// season container → episode item. It attaches **no Parts**, and that is the
// point rather than an omission: TMDB knows what exists, not where to get it, so
// a TMDB import is a described library with nothing to play until a stream
// source is installed alongside it.
func (c *Capability) Import(ctx context.Context, svc v1.ContentService, req v1.ImportRequest) (v1.ImportResult, error) {
	client, err := c.clientFrom(ctx, req.Settings)
	if err != nil {
		return v1.ImportResult{}, err
	}
	caller := req.Caller

	nativeID, nativeType, err := client.resolveRef(ctx, req.Ref)
	if err != nil {
		return v1.ImportResult{}, err
	}

	title, err := client.Detail(ctx, nativeType, nativeID)
	if err != nil {
		return v1.ImportResult{}, fmt.Errorf("fetch TMDB detail: %w", err)
	}

	// Dedup before writing, under both schemes. The TMDB id catches a re-import
	// through this module; the IMDb id catches the same title already
	// materialised by a source that keys on IMDb — without which adding
	// *Arrival* from TMDB after adding it from a Stremio addon would produce a
	// second Work for one film.
	if existing, found, err := c.find(ctx, svc, caller, title); err != nil {
		return v1.ImportResult{}, err
	} else if found {
		return v1.ImportResult{WorkID: existing, AlreadyKnown: true}, nil
	}

	name := title.Title
	if name == "" {
		name = nativeID
	}
	work, err := svc.AddContentWork(ctx, v1.AddContentWorkCommand{
		Caller:      caller,
		MediaType:   mediaTypeFor(nativeType),
		Title:       name,
		ExternalIDs: externalIDs(title),
		// Stored on the node rather than re-derived per read (ADR 0071). This is
		// the metadata the import already holds, so storing it costs nothing and
		// saves a provider round trip for every card that renders this title.
		Artwork: v1.Artwork{Poster: title.Poster, Backdrop: title.Backdrop, Logo: title.Logo},
	})
	if err != nil {
		return v1.ImportResult{}, fmt.Errorf("create work: %w", err)
	}
	result := v1.ImportResult{WorkID: work.Work.ID}

	if err := c.bind(ctx, svc, caller, work.Work.ID, title); err != nil {
		return v1.ImportResult{}, err
	}

	switch nativeType {
	case typeMovie:
		err = c.importFilm(ctx, svc, caller, work.Work.ID, &result)
	case typeTV:
		err = c.importSeries(ctx, svc, caller, work.Work.ID, title, &result)
	}
	if err != nil {
		return v1.ImportResult{}, err
	}

	v1.TelemetryFrom(ctx).Info("tmdb import complete",
		v1.String("native_type", nativeType),
		v1.String("native_id", nativeID),
		v1.Int("containers", result.Containers),
		v1.Int("items", result.Items))

	return result, nil
}

// importFilm builds a film as Work → feature item. A Part attaches to an item,
// never a work (ADR 0013), so the item exists even with nothing to attach — it
// is where a stream source will later hang a release.
func (c *Capability) importFilm(ctx context.Context, svc v1.ContentService, caller v1.Caller, workID v1.NodeID, result *v1.ImportResult) error {
	if _, err := svc.AddContentChild(ctx, v1.AddContentChildCommand{
		Caller: caller, ParentID: workID,
		Kind: v1.NodeItem, ItemType: v1.ItemFeature,
		Title: "Feature", NaturalOrder: 1,
	}); err != nil {
		return fmt.Errorf("create feature item: %w", err)
	}
	result.Items++
	return nil
}

// importSeries builds a series as Work → season container → episode item,
// grouping the flat episode list this module already ordered. Each episode
// carries its own still as artwork (ADR 0071: for an episode node, the poster
// slot is the still).
func (c *Capability) importSeries(ctx context.Context, svc v1.ContentService, caller v1.Caller, workID v1.NodeID, title Title, result *v1.ImportResult) error {
	for _, s := range groupBySeason(title.Episodes) {
		container, err := svc.AddContentChild(ctx, v1.AddContentChildCommand{
			Caller: caller, ParentID: workID,
			Kind: v1.NodeContainer, ContainerType: v1.ContainerSeason,
			Title: seasonTitle(s.number), NaturalOrder: float64(s.number),
		})
		if err != nil {
			return fmt.Errorf("create season %d: %w", s.number, err)
		}
		result.Containers++

		for _, e := range s.episodes {
			if _, err := svc.AddContentChild(ctx, v1.AddContentChildCommand{
				Caller: caller, ParentID: container.Node.ID,
				Kind: v1.NodeItem, ItemType: v1.ItemEpisode,
				Title: episodeTitle(e), NaturalOrder: float64(e.Episode),
				Artwork: v1.Artwork{Poster: e.Thumbnail},
			}); err != nil {
				return fmt.Errorf("create episode %d of season %d: %w", e.Episode, s.number, err)
			}
			result.Items++
		}
	}
	return nil
}

// find looks for a Work already bound to this title, under the TMDB id first and
// then the IMDb id. It returns the root work's id, since a match on a child
// would still mean the tree exists.
func (c *Capability) find(ctx context.Context, svc v1.ContentService, caller v1.Caller, title Title) (v1.NodeID, bool, error) {
	lookup := func(scheme, value string) (v1.NodeID, bool, error) {
		if value == "" {
			return "", false, nil
		}
		found, err := svc.FindContentByExternalID(ctx, v1.FindContentByExternalIDQuery{
			Caller: caller, Scheme: scheme, Value: value,
		})
		if err != nil {
			return "", false, fmt.Errorf("search existing content: %w", err)
		}
		for _, node := range found.Nodes {
			if node.IsRoot() {
				return node.ID, true, nil
			}
		}
		return "", false, nil
	}

	// Every scheme this module can bind, in descending order of how certainly it
	// identifies the same thing. Checking all of them is what stops a re-import
	// through a different source creating a duplicate.
	for _, candidate := range []struct{ scheme, value string }{
		{providerScheme, title.ID},
		{imdbScheme, title.IMDbID},
		{tvdbScheme, title.TVDbID},
	} {
		if id, ok, err := lookup(candidate.scheme, candidate.value); err != nil || ok {
			return id, ok, err
		}
	}
	return "", false, nil
}

// bind records the source bindings for a materialised work — TMDB always, IMDb
// when TMDB reported one. Both are exact external-id matches, so both are
// confirmed at full confidence rather than queued for review.
func (c *Capability) bind(ctx context.Context, svc v1.ContentService, caller v1.Caller, workID v1.NodeID, title Title) error {
	bindings := []struct{ provider, ref string }{{providerScheme, title.ID}}
	// Wikidata is deliberately absent: it goes in the external-id document but is
	// not a *source*, and a binding asserts that something can be sourced from
	// there.
	for _, extra := range []struct{ scheme, value string }{
		{imdbScheme, title.IMDbID},
		{tvdbScheme, title.TVDbID},
	} {
		if extra.value != "" {
			bindings = append(bindings, struct{ provider, ref string }{extra.scheme, extra.value})
		}
	}
	for _, b := range bindings {
		if _, err := svc.BindContentSource(ctx, v1.BindContentSourceCommand{
			Caller: caller, NodeID: workID,
			SourceProvider: b.provider, SourceRef: b.ref,
			MatchConfidence: 1, MatchMethod: v1.MatchExternalIDExact, Status: v1.BindingConfirmed,
		}); err != nil {
			return fmt.Errorf("bind %s source: %w", b.provider, err)
		}
	}
	return nil
}

// refFrom builds a ContentRef from a preview. The ref carries the TMDB id as the
// external identity the Platform dedups on, which is what makes a search result
// for a title already in the library read as *In library* rather than as new
// (ADR 0028).
func refFrom(p Preview) v1.ContentRef {
	return v1.ContentRef{
		Provider:       CapabilityID,
		NativeID:       p.ID,
		NativeType:     p.NativeType,
		MediaType:      mediaTypeFor(p.NativeType),
		ExternalScheme: providerScheme,
		ExternalID:     p.ID,
	}
}

// mediaTypeFor maps a TMDB content type to a Platform media type. TMDB has
// exactly two content types this module sources; anything else canonicalises as
// open text (ADR 0015) rather than being rejected.
func mediaTypeFor(nativeType string) v1.MediaType {
	switch nativeType {
	case typeMovie:
		return v1.MediaMovie
	case typeTV:
		return v1.MediaTVSeries
	default:
		return v1.NormaliseMediaType(nativeType)
	}
}

// externalIDs builds the Work's external-id document — the flat scheme-to-id
// shape FindContentByExternalID reads. Both ids go in when TMDB has both, so a
// later lookup under either scheme resolves.
func externalIDs(title Title) []byte {
	ids := map[string]string{providerScheme: title.ID}
	for scheme, value := range map[string]string{
		imdbScheme:     title.IMDbID,
		tvdbScheme:     title.TVDbID,
		wikidataScheme: title.WikidataID,
	} {
		if value != "" {
			ids[scheme] = value
		}
	}
	document, _ := json.Marshal(ids)
	return document
}

// seasonGroup collects one season's episodes.
type seasonGroup struct {
	number   int
	episodes []Episode
}

// groupBySeason collects an ordered episode list into ordered seasons. The list
// is already sorted by the client, so this preserves order rather than imposing
// one.
func groupBySeason(episodes []Episode) []seasonGroup {
	var groups []seasonGroup
	for _, e := range episodes {
		if n := len(groups); n > 0 && groups[n-1].number == e.Season {
			groups[n-1].episodes = append(groups[n-1].episodes, e)
			continue
		}
		groups = append(groups, seasonGroup{number: e.Season, episodes: []Episode{e}})
	}
	return groups
}

// seasonTitle names a season container. TMDB numbers specials as season 0, and
// calling that "Season 0" reads as a bug rather than as a convention.
func seasonTitle(number int) string {
	if number == 0 {
		return "Specials"
	}
	return fmt.Sprintf("Season %d", number)
}

// episodeTitle names an episode item, falling back to its number when TMDB has
// no title — which is normal for an episode that has not aired.
func episodeTitle(e Episode) string {
	if title := strings.TrimSpace(e.Title); title != "" {
		return title
	}
	return fmt.Sprintf("Episode %d", e.Episode)
}
