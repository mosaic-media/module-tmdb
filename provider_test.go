package tmdb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	tmdb "github.com/mosaic-media/module-tmdb"
	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

// The read roles: what they return, and the translations that are easy to get
// wrong because TMDB describes the same fact two different ways depending on
// whether the thing is a film or a series.

func TestSearchDropsPeopleAndMapsBothContentTypes(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	resp, err := capability.Search(context.Background(), v1.SearchRequest{
		Caller: v1.CallerFromSession("s-1"), Text: "blade", Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(resp.Results) != 2 {
		t.Fatalf("got %d results, want 2 (the person result must be dropped)", len(resp.Results))
	}

	film := resp.Results[0]
	if film.Title != "Blade Runner 2049" || film.Year != 2017 {
		t.Fatalf("film = %q (%d), want Blade Runner 2049 (2017)", film.Title, film.Year)
	}
	if film.Ref.MediaType != v1.MediaMovie || film.Ref.Provider != "tmdb" {
		t.Fatalf("film ref = %+v, want a tmdb movie ref", film.Ref)
	}
	if film.Ref.ExternalScheme != "tmdb" || film.Ref.ExternalID != "335984" {
		t.Fatalf("film external identity = %s/%s, want tmdb/335984", film.Ref.ExternalScheme, film.Ref.ExternalID)
	}

	// A series carries its title in `name` and its year in `first_air_date`.
	// Reading a series through the film field names is the classic TMDB bug and
	// yields an untitled result rather than an error.
	series := resp.Results[1]
	if series.Title != "Breaking Bad" || series.Year != 2008 {
		t.Fatalf("series = %q (%d), want Breaking Bad (2008)", series.Title, series.Year)
	}
	if series.Ref.MediaType != v1.MediaTVSeries {
		t.Fatalf("series media type = %q, want tv_series", series.Ref.MediaType)
	}
}

func TestSearchHonoursMediaTypeFilterAndLimit(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	filtered, err := capability.Search(context.Background(), v1.SearchRequest{
		Caller: v1.CallerFromSession("s-1"), Text: "blade",
		MediaType: v1.MediaTVSeries, Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(filtered.Results) != 1 || filtered.Results[0].Title != "Breaking Bad" {
		t.Fatalf("filtered results = %d, want only the series", len(filtered.Results))
	}

	limited, err := capability.Search(context.Background(), v1.SearchRequest{
		Caller: v1.CallerFromSession("s-1"), Text: "blade", Limit: 1, Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(limited.Results) != 1 {
		t.Fatalf("limited results = %d, want 1", len(limited.Results))
	}
}

func TestCatalogsAreCuratedAndAddressable(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	ctx := context.Background()

	resp, err := capability.Catalogs(ctx, v1.CatalogsRequest{
		Caller: v1.CallerFromSession("s-1"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Catalogs: %v", err)
	}
	if len(resp.Catalogs) == 0 {
		t.Fatal("no catalogs; a fresh install would have an empty home screen")
	}

	// Two catalogs share an id ("popular" for film and for television), so the
	// id alone is not a key and CatalogItems must take the type too.
	byKey := map[string]bool{}
	for _, c := range resp.Catalogs {
		key := c.ID + "/" + c.NativeType
		if byKey[key] {
			t.Errorf("duplicate catalog %q", key)
		}
		byKey[key] = true
		if c.Name == "" {
			t.Errorf("catalog %q has no name", key)
		}
	}

	items, err := capability.CatalogItems(ctx, v1.CatalogItemsRequest{
		Caller: v1.CallerFromSession("s-1"), CatalogID: "trending", NativeType: "movie", Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("CatalogItems: %v", err)
	}
	if len(items.Items) != 1 {
		t.Fatalf("got %d catalog items, want 1", len(items.Items))
	}
	// A list endpoint's results carry no media_type; the type must come from the
	// catalog declaration or every item would be untyped.
	if items.Items[0].Ref.MediaType != v1.MediaMovie {
		t.Fatalf("catalog item media type = %q, want movie", items.Items[0].Ref.MediaType)
	}
}

func TestCatalogItemsRejectsAnUnknownCatalog(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	_, err := capability.CatalogItems(context.Background(), v1.CatalogItemsRequest{
		Caller: v1.CallerFromSession("s-1"), CatalogID: "nonsense", NativeType: "movie", Settings: keySettings(),
	})
	if err == nil {
		t.Fatal("an unknown catalog id must be an error, not an empty page")
	}
}

func TestMetadataCarriesTheFieldsAnAddonCannot(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	if meta.Title != "Blade Runner 2049" || meta.Year != 2017 {
		t.Fatalf("title/year = %q/%d", meta.Title, meta.Year)
	}
	if meta.Rating != 7.6 {
		t.Fatalf("rating = %v, want 7.6 on TMDB's ten-point scale", meta.Rating)
	}
	// 164 minutes is 2h 44m. The contract is explicit that this is display text
	// rather than a duration, because sources disagree on the format.
	if meta.Runtime != "2h 44m" {
		t.Fatalf("runtime = %q, want 2h 44m", meta.Runtime)
	}
	if len(meta.Genres) != 2 {
		t.Fatalf("genres = %v, want two", meta.Genres)
	}

	// The clearlogo. A Stremio meta addon has nowhere to put one, which is the
	// recorded gap (ADR 0034) this module closes. The fake offers a
	// language-neutral logo and an English one; the English one wins because it
	// has been vetted as a title treatment for that language.
	if !strings.HasSuffix(meta.Logo, "/english.png") {
		t.Fatalf("logo = %q, want the language-tagged variant", meta.Logo)
	}

	// Cast with character names *and* headshots — the other recorded gap. Sorted
	// into billing order, which the fake deliberately does not supply.
	if len(meta.Cast) != 3 {
		t.Fatalf("cast = %d, want 3", len(meta.Cast))
	}
	lead := meta.Cast[0]
	if lead.Name != "Ryan Gosling" || lead.Role != "K" {
		t.Fatalf("lead = %q as %q, want Ryan Gosling as K in billing order", lead.Name, lead.Role)
	}
	if !strings.HasSuffix(lead.Photo, "/ryan.jpg") {
		t.Fatalf("lead photo = %q, want a headshot URL", lead.Photo)
	}
	for _, person := range meta.Cast {
		if person.Role == "" || person.Photo == "" {
			t.Errorf("%q has role %q and photo %q; both should be populated", person.Name, person.Role, person.Photo)
		}
	}

	// A film has no episode preview.
	if len(meta.Episodes) != 0 {
		t.Fatalf("a film returned %d episode previews", len(meta.Episodes))
	}
}

func TestMetadataForASeriesPreviewsEpisodesInOrder(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: seriesRef("1396"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	// A series' runtime comes from episode_run_time, which is a list rather than
	// the scalar a film has.
	if meta.Runtime != "49 min" {
		t.Fatalf("series runtime = %q, want 49 min from episode_run_time", meta.Runtime)
	}

	if len(meta.Episodes) != 4 {
		t.Fatalf("episode previews = %d, want 4", len(meta.Episodes))
	}
	want := []struct {
		season, episode int
		title           string
	}{
		{0, 1, "Good Cop Bad Cop"},
		{1, 1, "Pilot"},
		{1, 2, "Cat's in the Bag..."},
		{2, 1, "Episode 1"},
	}
	for i, w := range want {
		got := meta.Episodes[i]
		if got.Season != w.season || got.Episode != w.episode || got.Title != w.title {
			t.Errorf("episode %d = s%02de%02d %q, want s%02de%02d %q",
				i, got.Season, got.Episode, got.Title, w.season, w.episode, w.title)
		}
	}
	if !strings.HasSuffix(meta.Episodes[1].Thumbnail, "/s1e1.jpg") {
		t.Errorf("pilot thumbnail = %q, want the still URL", meta.Episodes[1].Thumbnail)
	}
}

func TestMetadataForAnUnknownTitleErrors(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	_, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("999999"), Settings: keySettings(),
	})
	if err == nil {
		t.Fatal("an unknown id must be an error, not an empty record")
	}
}

func TestUnsupportedNativeTypeIsRefusedBeforeTheAPICall(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	ref := movieRef("335984")
	ref.NativeType = "person"
	_, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: ref, Settings: keySettings(),
	})
	if err == nil || !strings.Contains(err.Error(), "person") {
		t.Fatalf("error = %v, want a refusal naming the unsupported type", err)
	}
}

func TestBearerTokenIsAcceptedAsWellAsAnAPIKey(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	// A v4 read access token is a JWT and goes in an Authorization header; a v3
	// key is hex and goes in the query string. A user copying from TMDB's
	// account page may arrive with either, so the shape decides rather than the
	// user — and the fake rejects a request carrying neither.
	token := []byte(`{"apiKey":"eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJ4In0.c2lnbmF0dXJl"}`)
	resp, err := capability.Search(context.Background(), v1.SearchRequest{
		Caller: v1.CallerFromSession("s-1"), Text: "blade", Settings: token,
	})
	if err != nil {
		t.Fatalf("Search with a v4 token: %v", err)
	}
	if len(resp.Results) == 0 {
		t.Fatal("a v4 token returned nothing")
	}
}

func TestMalformedSettingsAreReportedNotIgnored(t *testing.T) {
	capability := tmdb.New(nil)

	_, err := capability.Search(context.Background(), v1.SearchRequest{
		Caller: v1.CallerFromSession("s-1"), Text: "blade", Settings: []byte(`{"apiKey":`),
	})
	if err == nil {
		t.Fatal("a malformed settings document must be an error; silently treating it as empty hides a bad write")
	}
}

// The endpoints added after the first release. Each is here because it either
// closes a gap ADR 0034 recorded or removes a limit the first version shipped
// with.

func TestMetadataCarriesKeywordsCertificationTrailersAndSimilar(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"),
		Settings: []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","region":"GB"}`),
	})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	if len(meta.Keywords) != 2 || meta.Keywords[0] != "dystopia" {
		t.Errorf("keywords = %v, want the film's own list", meta.Keywords)
	}
	// GB is configured, so the GB certification is used — not the US "R" that
	// appears first in the response.
	if meta.Certification != "15" {
		t.Errorf("certification = %q, want the configured region's 15", meta.Certification)
	}

	if len(meta.Trailers) != 1 {
		t.Fatalf("trailers = %d, want 1 (the featurette is not a trailer)", len(meta.Trailers))
	}
	trailer := meta.Trailers[0]
	if trailer.Site != "YouTube" || trailer.Key != "trail" || !trailer.Official {
		t.Errorf("trailer = %+v", trailer)
	}

	if len(meta.Similar) != 1 || meta.Similar[0].Title != "Blade Runner" {
		t.Fatalf("similar = %+v, want the recommendation", meta.Similar)
	}
	// A related item must be openable, which means carrying a usable ref.
	if meta.Similar[0].Ref.Provider != "tmdb" || meta.Similar[0].Ref.NativeID != "78" {
		t.Errorf("similar ref = %+v", meta.Similar[0].Ref)
	}
	if meta.Similar[0].InLibrary || meta.Similar[0].NodeID != "" {
		t.Error("a provider must leave InLibrary/NodeID for the Platform to fill (ADR 0028)")
	}
}

func TestMetadataResolvesTheFranchiseInReleaseOrder(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	if meta.Collection == nil {
		t.Fatal("no collection; the franchise is one of ADR 0034's recorded gaps")
	}
	if meta.Collection.Name != "Blade Runner Collection" {
		t.Errorf("collection name = %q", meta.Collection.Name)
	}
	if len(meta.Collection.Items) != 2 {
		t.Fatalf("collection members = %d, want 2", len(meta.Collection.Items))
	}
	// TMDB returns members in popularity order; a franchise rail wants them
	// chronological.
	if meta.Collection.Items[0].Year != 1982 || meta.Collection.Items[1].Year != 2017 {
		t.Errorf("collection order = %d then %d, want chronological",
			meta.Collection.Items[0].Year, meta.Collection.Items[1].Year)
	}
	// The list includes the title being described, so a consumer wanting "the
	// others" filters on the ref it already holds.
	if meta.Collection.Items[1].Ref.NativeID != "335984" {
		t.Errorf("the described film is not in its own collection: %+v", meta.Collection.Items)
	}
}

func TestASeriesHasNoCollectionAndCarriesItsTVDbID(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	content := newFakeContent()

	result, err := capability.Import(context.Background(), content, v1.ImportRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: seriesRef("1396"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	bound := map[string]string{}
	for _, b := range content.binds {
		bound[b.SourceProvider] = b.SourceRef
	}
	// TVDB is television-only, and a TV-oriented source keys on it — without this
	// binding the same series added from one would be a second Work.
	if bound["tvdb"] != "81189" {
		t.Errorf("tvdb binding = %q, want 81189", bound["tvdb"])
	}
	if bound["imdb"] != "tt0903747" || bound["tmdb"] != "1396" {
		t.Errorf("bindings = %v", bound)
	}
	// Wikidata goes in the external-id document but is never bound: nothing
	// sources content from it, and a binding asserts that something can.
	if _, ok := bound["wikidata"]; ok {
		t.Error("wikidata was bound as a source")
	}

	var ids map[string]string
	if err := json.Unmarshal(content.nodes[result.WorkID].ExternalIDs, &ids); err != nil {
		t.Fatalf("external ids: %v", err)
	}
	if ids["tvdb"] != "81189" {
		t.Errorf("external ids = %v, want the tvdb id recorded", ids)
	}
}

func TestMetadataAnswersForAnIMDbKeyedRef(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	// The ref another module would produce: Cinemeta and every Stremio addon key
	// on IMDb ids, and under ADR 0072 a credential-free IMDb-keyed source is the
	// guaranteed floor. Without the reverse lookup this module could not describe
	// a single work in such a library.
	ref := v1.ContentRef{
		Provider: "cinemeta", NativeID: "tt1856101", NativeType: "movie",
		MediaType: v1.MediaMovie, ExternalScheme: "imdb", ExternalID: "tt1856101",
	}

	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: ref, Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Metadata for an IMDb ref: %v", err)
	}
	if meta.Title != "Blade Runner 2049" {
		t.Fatalf("title = %q", meta.Title)
	}
	// The ref echoes back unchanged: the caller addressed this item, and handing
	// back a different identity would break the screen that asked.
	if meta.Ref.NativeID != "tt1856101" {
		t.Errorf("ref = %+v, want the caller's own ref echoed", meta.Ref)
	}
}

func TestAnUnknownIMDbIDIsAClearError(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	ref := movieRef("tt0000000")
	_, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: ref, Settings: keySettings(),
	})
	if err == nil || !strings.Contains(err.Error(), "tt0000000") {
		t.Fatalf("error = %v, want one naming the unresolvable id", err)
	}
}

func TestCustomDiscoverCatalogs(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	ctx := context.Background()
	caller := v1.CallerFromSession("s-1")

	settings := []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","catalogs":[
		{"name":"French Thrillers","type":"movie","query":"with_genres=53&with_original_language=fr"},
		{"name":"Recent Sci-Fi","type":"tv","query":"with_genres=10765"}
	]}`)

	resp, err := capability.Catalogs(ctx, v1.CatalogsRequest{Caller: caller, Settings: settings})
	if err != nil {
		t.Fatalf("Catalogs: %v", err)
	}

	var names []string
	for _, c := range resp.Catalogs {
		names = append(names, c.Name)
	}
	if !containsString(names, "French Thrillers") || !containsString(names, "Recent Sci-Fi") {
		t.Fatalf("catalogs = %v, want the user's own alongside the built-ins", names)
	}

	// Address the custom catalog. The fake refuses a request whose paging or
	// credential the user's query overrode.
	var custom v1.Catalog
	for _, c := range resp.Catalogs {
		if c.Name == "French Thrillers" {
			custom = c
		}
	}
	items, err := capability.CatalogItems(ctx, v1.CatalogItemsRequest{
		Caller: caller, CatalogID: custom.ID, NativeType: custom.NativeType, Settings: settings,
	})
	if err != nil {
		t.Fatalf("CatalogItems for a custom catalog: %v", err)
	}
	if len(items.Items) != 1 || items.Items[0].Ref.MediaType != v1.MediaMovie {
		t.Fatalf("custom catalog items = %+v", items.Items)
	}
}

// A discover query is free text from a settings screen, appended to a request
// carrying the credential. The fake 403s if the credential was overridden.
func TestACustomCatalogQueryCannotOverrideTheCredential(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	caller := v1.CallerFromSession("s-1")

	settings := []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","catalogs":[
		{"name":"Hostile","type":"movie","query":"api_key=attacker&page=9&with_genres=53"}
	]}`)

	resp, err := capability.Catalogs(context.Background(), v1.CatalogsRequest{Caller: caller, Settings: settings})
	if err != nil {
		t.Fatalf("Catalogs: %v", err)
	}
	var hostile v1.Catalog
	for _, c := range resp.Catalogs {
		if c.Name == "Hostile" {
			hostile = c
		}
	}
	if hostile.ID == "" {
		t.Fatal("the catalog was dropped entirely; it should survive with its reserved parameters stripped")
	}

	items, err := capability.CatalogItems(context.Background(), v1.CatalogItemsRequest{
		Caller: caller, CatalogID: hostile.ID, NativeType: hostile.NativeType, Settings: settings,
	})
	if err != nil {
		t.Fatalf("CatalogItems: %v — the user's api_key or page reached the request", err)
	}
	if len(items.Items) != 1 {
		t.Fatalf("items = %d", len(items.Items))
	}
}

func TestImageBaseComesFromConfiguration(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	// TMDB publishes the CDN base rather than promising the hardcoded one holds
	// forever; the fake serves a different host so a regression to the constant
	// is visible.
	if !strings.HasPrefix(meta.Poster, "https://fake-cdn.example/t/p/") {
		t.Fatalf("poster = %q, want the configured CDN base", meta.Poster)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// Watch providers: where a title can be seen *outside* Mosaic. The tests lean on
// the two properties that make it different from every other read field — it is
// region-exact, and it is not a source.

func TestWatchProvidersAreRegionExact(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"),
		Settings: []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","region":"GB"}`),
	})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}

	if meta.Watch == nil {
		t.Fatal("no watch availability for a region TMDB has offers in")
	}
	if meta.Watch.Region != "GB" {
		t.Fatalf("region = %q, want the configured GB", meta.Watch.Region)
	}
	if !strings.Contains(meta.Watch.Link, "locale=GB") {
		t.Fatalf("link = %q, want the GB page", meta.Watch.Link)
	}
	// TMDB's availability data is JustWatch's, and the terms require crediting
	// them wherever it is shown — so it travels in the value rather than being
	// something a screen has to remember.
	if meta.Watch.Attribution != "JustWatch" {
		t.Errorf("attribution = %q", meta.Watch.Attribution)
	}

	// Subscription first — what a viewer may already pay for, before what costs
	// money now — and within that, the source's own display priority.
	if len(meta.Watch.Offers) != 3 {
		t.Fatalf("offers = %+v, want 3 distinct services", meta.Watch.Offers)
	}
	if meta.Watch.Offers[0].Provider != "Prime Video" || meta.Watch.Offers[0].Type != v1.WatchSubscription {
		t.Errorf("first offer = %+v, want Prime Video on subscription (display_priority 0)", meta.Watch.Offers[0])
	}
	if meta.Watch.Offers[1].Provider != "Netflix" || meta.Watch.Offers[1].Type != v1.WatchSubscription {
		t.Errorf("second offer = %+v, want Netflix on subscription", meta.Watch.Offers[1])
	}
	// Netflix is also listed for rent. The better terms won and it appears once.
	if meta.Watch.Offers[2].Provider != "Apple TV" || meta.Watch.Offers[2].Type != v1.WatchRent {
		t.Errorf("third offer = %+v, want Apple TV to rent", meta.Watch.Offers[2])
	}
	if meta.Watch.Offers[0].Logo == "" {
		t.Error("no provider logo; a service row is a logo row")
	}

	// A different region is a different answer, not a translated one.
	us, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"),
		Settings: []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","region":"US"}`),
	})
	if err != nil {
		t.Fatalf("Metadata (US): %v", err)
	}
	if us.Watch == nil || len(us.Watch.Offers) != 1 || us.Watch.Offers[0].Provider != "Max" {
		t.Fatalf("US offers = %+v, want only Max", us.Watch)
	}
}

func TestWatchProvidersWithoutARegionMakeNoClaim(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	// TMDB returns over a hundred regions for a well-distributed film. Picking
	// one because none was configured would be inventing an answer, and telling a
	// viewer in Britain that something is on a service carrying it only in the
	// United States is worse than telling them nothing.
	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Watch != nil {
		t.Fatalf("watch = %+v with no region configured, want nil", meta.Watch)
	}
}

func TestARegionWithNoOffersIsStillAnAnswer(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))

	// "None known here" is a different fact from "no data", and a detail screen
	// renders them differently.
	meta, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"),
		Settings: []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","region":"IE"}`),
	})
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	if meta.Watch == nil {
		t.Fatal("a region with a link but no offers returned nil; that erases 'none known here'")
	}
	if len(meta.Watch.Offers) != 0 {
		t.Fatalf("offers = %+v, want none", meta.Watch.Offers)
	}
	if meta.Watch.Link == "" {
		t.Error("no link, so there is nothing an informational control could open")
	}

	// A region TMDB does not report at all is nil, not an empty shell.
	absent, err := capability.Metadata(context.Background(), v1.MetadataRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"),
		Settings: []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","region":"JP"}`),
	})
	if err != nil {
		t.Fatalf("Metadata (JP): %v", err)
	}
	if absent.Watch != nil {
		t.Fatalf("watch = %+v for an unreported region, want nil", absent.Watch)
	}
}

// The boundary that matters most: availability is not a source. An offer must
// never become something the Platform thinks it can play.
func TestWatchProvidersNeverBecomeParts(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	content := newFakeContent()

	result, err := capability.Import(context.Background(), content, v1.ImportRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"),
		Settings: []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","region":"GB"}`),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.Parts != 0 || len(content.parts) != 0 {
		t.Fatalf("import attached %d parts; a watch offer is not a playable location", len(content.parts))
	}
	// Nor a source binding: Mosaic cannot source anything from Netflix.
	for _, b := range content.binds {
		if b.SourceProvider != "tmdb" && b.SourceProvider != "imdb" && b.SourceProvider != "tvdb" {
			t.Errorf("unexpected binding %q; watch providers must not be bound as sources", b.SourceProvider)
		}
	}
}

// Availability is *stored* on the node, not only projected onto a detail. This
// is what makes grouping a library by service possible at all: the question is
// asked across the whole library, and a round trip per title cannot answer it.

func TestImportStoresWatchAvailabilityOnTheNode(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	content := newFakeContent()

	result, err := capability.Import(context.Background(), content, v1.ImportRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"),
		Settings: []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","region":"GB"}`),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	var document map[string]struct {
		Version   int      `json:"version"`
		Region    string   `json:"region"`
		CheckedAt string   `json:"checkedAt"`
		Providers []string `json:"providers"`
		Offers    []struct {
			Provider string `json:"provider"`
			Type     string `json:"type"`
		} `json:"offers"`
	}
	if err := json.Unmarshal(content.nodes[result.WorkID].Attributes, &document); err != nil {
		t.Fatalf("attributes are not the expected document: %v", err)
	}

	stored, ok := document[tmdb.WatchAttribute]
	if !ok {
		t.Fatalf("no %q key in attributes: %s", tmdb.WatchAttribute, content.nodes[result.WorkID].Attributes)
	}
	if stored.Version != tmdb.WatchAttributeVersion {
		t.Errorf("version = %d, want %d", stored.Version, tmdb.WatchAttributeVersion)
	}
	if stored.Region != "GB" {
		t.Errorf("region = %q; availability is meaningless without the region it applies to", stored.Region)
	}

	// The flat provider array is what a containment query matches — the richer
	// offers cannot be asked of an index this way.
	want := []string{"Prime Video", "Netflix", "Apple TV"}
	if len(stored.Providers) != len(want) {
		t.Fatalf("providers = %v, want %v", stored.Providers, want)
	}
	for i, name := range want {
		if stored.Providers[i] != name {
			t.Errorf("provider %d = %q, want %q", i, stored.Providers[i], name)
		}
	}
	if len(stored.Offers) != 3 || stored.Offers[0].Type != "subscription" {
		t.Errorf("offers = %+v, want the terms alongside the names", stored.Offers)
	}

	// Nothing refreshes this, so a consumer must be able to say how old it is.
	if _, err := time.Parse(time.RFC3339, stored.CheckedAt); err != nil {
		t.Errorf("checkedAt = %q, not an RFC3339 timestamp: %v", stored.CheckedAt, err)
	}
}

func TestImportStoresNoAttributesWithoutAvailability(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	content := newFakeContent()

	// No region, so no availability — and therefore no empty shell that a
	// containment query would have to reason about.
	result, err := capability.Import(context.Background(), content, v1.ImportRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if attributes := content.nodes[result.WorkID].Attributes; len(attributes) != 0 {
		t.Fatalf("attributes = %s, want none", attributes)
	}
}

// The shape a containment query is written against. It is exported precisely
// because it is a published key rather than a private one, and this test is what
// stops it drifting silently.
func TestTheStoredShapeIsQueryableByContainment(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	content := newFakeContent()

	if _, err := capability.Import(context.Background(), content, v1.ImportRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"),
		Settings: []byte(`{"apiKey":"0123456789abcdef0123456789abcdef","region":"GB"}`),
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}

	// This is the filter a caller would pass as SearchContentQuery.
	// AttributesContain. Asserting it against the document the module actually
	// wrote is what keeps the two from drifting apart — they live in different
	// repositories and nothing else connects them.
	filter := []byte(`{"` + tmdb.WatchAttribute + `":{"providers":["Netflix"]}}`)
	if !jsonContains(t, content.nodes[importedWorkID(t, content)].Attributes, filter) {
		t.Fatalf("the stored document does not satisfy the documented query shape\n stored: %s\n filter: %s",
			content.nodes[importedWorkID(t, content)].Attributes, filter)
	}
}

func importedWorkID(t *testing.T, content *fakeContent) v1.NodeID {
	t.Helper()
	for _, id := range content.order {
		if content.nodes[id].IsRoot() {
			return id
		}
	}
	t.Fatal("no work was imported")
	return ""
}

// jsonContains is a minimal stand-in for the engine's containment operator:
// every key and array member in want must be present in got. It is not a general
// implementation — it covers objects, string arrays and scalars, which is what
// the documented filter shape uses.
func jsonContains(t *testing.T, gotDoc, wantDoc []byte) bool {
	t.Helper()
	var got, want any
	if err := json.Unmarshal(gotDoc, &got); err != nil {
		t.Fatalf("stored document is not JSON: %v", err)
	}
	if err := json.Unmarshal(wantDoc, &want); err != nil {
		t.Fatalf("filter is not JSON: %v", err)
	}
	return contains(got, want)
}

func contains(got, want any) bool {
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range w {
			if !contains(g[key], value) {
				return false
			}
		}
		return true
	case []any:
		g, ok := got.([]any)
		if !ok {
			return false
		}
		for _, value := range w {
			found := false
			for _, candidate := range g {
				if contains(candidate, value) {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		return true
	default:
		return got == want
	}
}
