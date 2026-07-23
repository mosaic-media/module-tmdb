package tmdb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
