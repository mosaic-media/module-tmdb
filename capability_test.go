package tmdb_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tmdb "github.com/mosaic-media/module-tmdb"
	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

func TestManifestDeclaresOnlyRolesItImplements(t *testing.T) {
	manifest := tmdb.New(nil).Manifest()

	if manifest.ID != "tmdb" {
		t.Fatalf("manifest id = %q, want tmdb", manifest.ID)
	}

	// The Platform verifies this at composition and fails boot on a mismatch
	// (ADR 0027); asserting it here means the failure is a test rather than a
	// refusal to start.
	capability := any(tmdb.New(nil))
	backed := map[v1.Role]bool{
		v1.RoleMetadata:   func() bool { _, ok := capability.(v1.MetadataProvider); return ok }(),
		v1.RoleSearch:     func() bool { _, ok := capability.(v1.SearchProvider); return ok }(),
		v1.RoleCatalog:    func() bool { _, ok := capability.(v1.CatalogProvider); return ok }(),
		v1.RoleSettingsUI: func() bool { _, ok := capability.(v1.SettingsUIProvider); return ok }(),
	}
	for _, role := range manifest.Provides {
		if !backed[role] {
			t.Errorf("manifest declares role %q with no provider interface behind it", role)
		}
	}

	// TMDB describes content; it does not host or index it. Declaring a stream
	// role would make the Platform offer a play affordance nothing can satisfy.
	for _, role := range manifest.Provides {
		if role == v1.RoleStream || role == v1.RoleSubtitles {
			t.Errorf("manifest declares %q; TMDB has no streams or subtitles", role)
		}
	}
}

func TestImportFilm(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	content := newFakeContent()

	result, err := capability.Import(context.Background(), content, v1.ImportRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if result.AlreadyKnown {
		t.Fatal("a first import must not be AlreadyKnown")
	}
	work := content.nodes[result.WorkID]
	if work.Kind != v1.NodeWork || work.MediaType != v1.MediaMovie {
		t.Fatalf("work kind/media = %q/%q, want work/movie", work.Kind, work.MediaType)
	}
	if work.Title != "Blade Runner 2049" {
		t.Fatalf("work title = %q, want the TMDB title", work.Title)
	}

	// A film is Work -> feature item, and there are no Parts: TMDB knows what
	// exists, not where to get it.
	if result.Items != 1 || result.Containers != 0 || result.Parts != 0 {
		t.Fatalf("counts = items %d containers %d parts %d, want 1/0/0", result.Items, result.Containers, result.Parts)
	}
	if len(content.parts) != 0 {
		t.Fatalf("attached %d parts; a metadata source must attach none", len(content.parts))
	}

	// Artwork is stored on the node rather than re-derived per read (ADR 0071).
	if !strings.HasSuffix(work.Artwork.Poster, "/poster.jpg") || !strings.HasSuffix(work.Artwork.Backdrop, "/backdrop.jpg") {
		t.Fatalf("work artwork = %+v, want the poster and backdrop stored", work.Artwork)
	}
	if work.Artwork.Logo == "" {
		t.Fatal("work artwork carries no logo; the clearlogo is one of the two fields this module exists for")
	}
}

func TestImportBindsBothTMDBAndIMDbSoOtherSourcesDedup(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	content := newFakeContent()

	result, err := capability.Import(context.Background(), content, v1.ImportRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"), Settings: keySettings(),
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	bound := map[string]string{}
	for _, b := range content.binds {
		bound[b.SourceProvider] = b.SourceRef
		if b.Status != v1.BindingConfirmed || b.MatchMethod != v1.MatchExternalIDExact {
			t.Errorf("binding %q status/method = %q/%q, want confirmed/external_id_exact", b.SourceProvider, b.Status, b.MatchMethod)
		}
	}
	if bound["tmdb"] != "335984" {
		t.Errorf("tmdb binding = %q, want 335984", bound["tmdb"])
	}
	// Without this, the same film added from a source that keys on IMDb ids —
	// every Stremio addon — would produce a second Work.
	if bound["imdb"] != "tt1856101" {
		t.Errorf("imdb binding = %q, want tt1856101", bound["imdb"])
	}

	var ids map[string]string
	if err := json.Unmarshal(content.nodes[result.WorkID].ExternalIDs, &ids); err != nil {
		t.Fatalf("external ids are not the flat scheme-to-id document: %v", err)
	}
	if ids["tmdb"] != "335984" || ids["imdb"] != "tt1856101" {
		t.Fatalf("external ids = %v, want both schemes", ids)
	}
}

func TestImportIsIdempotentUnderEitherScheme(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	content := newFakeContent()
	request := v1.ImportRequest{
		Caller: v1.CallerFromSession("s-1"), Ref: movieRef("335984"), Settings: keySettings(),
	}

	first, err := capability.Import(context.Background(), content, request)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	second, err := capability.Import(context.Background(), content, request)
	if err != nil {
		t.Fatalf("second Import: %v", err)
	}

	if !second.AlreadyKnown || second.WorkID != first.WorkID {
		t.Fatalf("second import = %+v, want AlreadyKnown on %s", second, first.WorkID)
	}
	if second.Items != 0 || second.Containers != 0 {
		t.Fatalf("second import created %d items and %d containers, want none", second.Items, second.Containers)
	}
}

func TestImportSeriesBuildsSeasonsAndEpisodes(t *testing.T) {
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

	// The fake declares four seasons, one of which has no episodes and must not
	// be fetched or created: specials + two real seasons, four episodes.
	if result.Containers != 3 || result.Items != 4 {
		t.Fatalf("counts = containers %d items %d, want 3/4", result.Containers, result.Items)
	}

	seasons := content.childrenOf(result.WorkID)
	if len(seasons) != 3 {
		t.Fatalf("work has %d children, want 3 seasons", len(seasons))
	}
	// TMDB numbers specials as season 0; "Season 0" reads as a bug.
	if seasons[0].Title != "Specials" || seasons[0].NaturalOrder != 0 {
		t.Fatalf("first season = %q at order %v, want Specials at 0", seasons[0].Title, seasons[0].NaturalOrder)
	}
	if seasons[1].Title != "Season 1" || seasons[1].ContainerType != v1.ContainerSeason {
		t.Fatalf("second season = %q (%q), want Season 1 container", seasons[1].Title, seasons[1].ContainerType)
	}

	// The fake serves season 1 out of order; the tree must not be.
	episodes := content.childrenOf(seasons[1].ID)
	if len(episodes) != 2 || episodes[0].Title != "Pilot" || episodes[1].Title != "Cat's in the Bag..." {
		t.Fatalf("season 1 episodes = %v, want Pilot then Cat's in the Bag...", titlesOf(episodes))
	}
	if episodes[0].Kind != v1.NodeItem || episodes[0].ItemType != v1.ItemEpisode {
		t.Fatalf("episode kind/type = %q/%q, want item/episode", episodes[0].Kind, episodes[0].ItemType)
	}
	// For an episode node the poster slot is the still (ADR 0071).
	if !strings.HasSuffix(episodes[0].Artwork.Poster, "/s1e1.jpg") {
		t.Fatalf("episode artwork = %+v, want the still stored as the poster", episodes[0].Artwork)
	}

	// An untitled episode falls back to its number rather than materialising a
	// node with an empty title.
	untitled := content.childrenOf(seasons[2].ID)
	if len(untitled) != 1 || untitled[0].Title != "Episode 1" {
		t.Fatalf("season 2 episodes = %v, want the numbered fallback", titlesOf(untitled))
	}
}

func TestEveryRoleRefusesWithoutAnAPIKey(t *testing.T) {
	server := fakeTMDB()
	defer server.Close()
	capability := tmdb.New(redirect(server))
	ctx := context.Background()
	caller := v1.CallerFromSession("s-1")

	// An empty document and an explicitly blank key are the same state, and a
	// user reaches both.
	for _, settings := range [][]byte{nil, []byte(`{}`), []byte(`{"apiKey":"  "}`)} {
		calls := map[string]error{}
		_, calls["Metadata"] = capability.Metadata(ctx, v1.MetadataRequest{Caller: caller, Ref: movieRef("335984"), Settings: settings})
		_, calls["Search"] = capability.Search(ctx, v1.SearchRequest{Caller: caller, Text: "blade", Settings: settings})
		_, calls["Catalogs"] = capability.Catalogs(ctx, v1.CatalogsRequest{Caller: caller, Settings: settings})
		_, calls["CatalogItems"] = capability.CatalogItems(ctx, v1.CatalogItemsRequest{Caller: caller, CatalogID: "trending", NativeType: "movie", Settings: settings})
		_, calls["Import"] = capability.Import(ctx, newFakeContent(), v1.ImportRequest{Caller: caller, Ref: movieRef("335984"), Settings: settings})

		for name, err := range calls {
			if err == nil {
				t.Errorf("%s with settings %q succeeded; TMDB has no anonymous access", name, settings)
				continue
			}
			if !strings.Contains(err.Error(), "API key") {
				t.Errorf("%s error = %q, want it to name the missing API key", name, err)
			}
		}
	}
}

// The settings screen is the only path to setting a key, so it must render for
// a user who has none — the state in which every other role refuses.
func TestSettingsUIRendersWithNoKeySet(t *testing.T) {
	capability := tmdb.New(nil)

	for _, settings := range [][]byte{nil, keySettings()} {
		resp, err := capability.SettingsUI(context.Background(), v1.SettingsUIRequest{
			Caller: v1.CallerFromSession("s-1"), Settings: settings,
		})
		if err != nil {
			t.Fatalf("SettingsUI with %q: %v", settings, err)
		}
		if len(resp.UI) == 0 {
			t.Fatalf("SettingsUI with %q returned no screen", settings)
		}
		var screen map[string]any
		if err := json.Unmarshal(resp.UI, &screen); err != nil {
			t.Fatalf("settings screen is not valid UINode JSON: %v", err)
		}
	}
}

// A settings screen is a page a user may screenshot when asking for help, so the
// key must never be *rendered*. It is masked to its last four characters.
//
// It is not absent from the screen, and this test asserts the strongest thing
// that is actually true rather than the thing one would want. configureModule
// replaces the settings document wholesale (ADR 0021, no partial update), so
// every control that changes a language or a region must carry the key with it
// or erase it — see configureInput for why that is recorded as a gap rather than
// worked around. What this pins down is that the credential lives only inside
// action payloads and never in a display property, which is the boundary that
// can be held without a change to the Platform.
func TestSettingsUIRendersTheKeyOnlyMasked(t *testing.T) {
	const key = "0123456789abcdef0123456789abcdef"
	capability := tmdb.New(nil)

	resp, err := capability.SettingsUI(context.Background(), v1.SettingsUIRequest{
		Caller: v1.CallerFromSession("s-1"), Settings: []byte(`{"apiKey":"` + key + `"}`),
	})
	if err != nil {
		t.Fatalf("SettingsUI: %v", err)
	}

	var screen any
	if err := json.Unmarshal(resp.UI, &screen); err != nil {
		t.Fatalf("settings screen is not valid JSON: %v", err)
	}
	displayed, err := json.Marshal(withoutActions(screen))
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	if strings.Contains(string(displayed), key) {
		t.Fatal("the API key appears in a display property of the settings screen")
	}
	if !strings.Contains(string(displayed), "••••cdef") {
		t.Fatal("the settings screen does not show the masked key, so a user cannot tell a key is set")
	}

	// The replace-key field carries the "$value" placeholder rather than the
	// current key, so setting a new one does not require echoing the old one.
	if !strings.Contains(string(resp.UI), "$value") {
		t.Fatal("the settings screen has no $value placeholder; the key field cannot submit")
	}
}

// withoutActions strips every "action" property from a UINode tree, leaving what
// the screen actually displays.
func withoutActions(node any) any {
	switch n := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(n))
		for key, value := range n {
			if key == "action" {
				continue
			}
			out[key] = withoutActions(value)
		}
		return out
	case []any:
		out := make([]any, 0, len(n))
		for _, value := range n {
			out = append(out, withoutActions(value))
		}
		return out
	default:
		return node
	}
}

func titlesOf(nodes []v1.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, n.Title)
	}
	return out
}
