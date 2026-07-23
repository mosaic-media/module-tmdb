package tmdb

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

// Internal tests for the translations that have no observable surface of their
// own but decide what every role returns.

// testClient is a client with no credential and the default CDN layout — enough
// for every translation below, none of which makes a request.
func testClient() *Client { return &Client{images: defaultImageConfig} }

func TestRuntimeLabel(t *testing.T) {
	cases := []struct {
		name     string
		movie    int
		episodes []int
		want     string
	}{
		{"a film under an hour", 42, nil, "42 min"},
		{"a film on the hour", 120, nil, "2h"},
		{"a film with minutes over", 164, nil, "2h 44m"},
		{"a series falls back to its episode runtime", 0, []int{49}, "49 min"},
		{"a series with several declared runtimes takes the first", 0, []int{22, 44}, "22 min"},
		{"nothing known is empty, not zero", 0, nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runtimeLabel(c.movie, c.episodes); got != c.want {
				t.Fatalf("runtimeLabel(%d, %v) = %q, want %q", c.movie, c.episodes, got, c.want)
			}
		})
	}
}

func TestImageURL(t *testing.T) {
	c := testClient()
	want := defaultImageConfig.base + defaultImageConfig.poster + "/abc.jpg"
	if got := c.imageURL("/abc.jpg", c.images.poster); got != want {
		t.Fatalf("imageURL = %q, want %q", got, want)
	}
	// An absent asset must stay absent. A URL to nothing renders as a broken
	// image, which is worse than a fallback.
	for _, path := range []string{"", "   "} {
		if got := c.imageURL(path, c.images.poster); got != "" {
			t.Fatalf("imageURL(%q) = %q, want empty", path, got)
		}
	}
}

func TestPickSizeNeverFallsBackToOriginal(t *testing.T) {
	// "original" is unbounded. A poster rail that silently started serving 4000px
	// source scans would be a performance regression nothing reported.
	if got := pickSize([]string{"w92", "w500", "original"}, "w500"); got != "w500" {
		t.Fatalf("pickSize kept-preferred = %q", got)
	}
	if got := pickSize([]string{"w92", "w342", "original"}, "w500"); got != "w342" {
		t.Fatalf("pickSize fallback = %q, want the largest non-original", got)
	}
	if got := pickSize(nil, "w500"); got != "w500" {
		t.Fatalf("pickSize of an empty list = %q", got)
	}
	if got := pickSize([]string{"original"}, "w500"); got != "w500" {
		t.Fatalf("pickSize of original-only = %q, want the preferred default", got)
	}
}

func TestParseYear(t *testing.T) {
	cases := map[string]int{
		"2017-10-04": 2017,
		"2008":       2008,
		"":           0,
		"soon":       0,
		"20":         0,
	}
	for date, want := range cases {
		if got := parseYear(date); got != want {
			t.Errorf("parseYear(%q) = %d, want %d", date, got, want)
		}
	}
}

func TestIsBearerToken(t *testing.T) {
	// A v4 read access token is a three-segment JWT; a v3 API key is 32 hex
	// characters. Getting this backwards sends the credential in the wrong place
	// and TMDB answers 401 with no hint as to why.
	if !isBearerToken("eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJ4In0.sig") {
		t.Error("a JWT was not recognised as a bearer token")
	}
	for _, key := range []string{"0123456789abcdef0123456789abcdef", "", "a.b"} {
		if isBearerToken(key) {
			t.Errorf("%q was treated as a bearer token", key)
		}
	}
}

func TestImageLanguagesAlwaysIncludesNeutralAssets(t *testing.T) {
	// Without the explicit null, TMDB returns only assets tagged with the
	// request language — and most clearlogos are untagged, so a show appears to
	// have none.
	cases := map[string]string{
		"en-US": "en,null",
		"de-DE": "de,en,null",
		"":      "en,null",
	}
	for language, want := range cases {
		if got := imageLanguages(language); got != want {
			t.Errorf("imageLanguages(%q) = %q, want %q", language, got, want)
		}
	}
}

func TestPickLogo(t *testing.T) {
	c := testClient()

	t.Run("prefers a language-tagged logo over an untagged one", func(t *testing.T) {
		got := c.pickLogo([]rawImage{
			{FilePath: "/neutral.png", VoteAverage: 9},
			{FilePath: "/english.png", ISO639: "en", VoteAverage: 1},
		})
		if got != c.imageURL("/english.png", c.images.logo) {
			t.Fatalf("pickLogo = %q", got)
		}
	})

	t.Run("breaks a tie on votes", func(t *testing.T) {
		got := c.pickLogo([]rawImage{
			{FilePath: "/low.png", ISO639: "en", VoteAverage: 2},
			{FilePath: "/high.png", ISO639: "en", VoteAverage: 8},
		})
		if got != c.imageURL("/high.png", c.images.logo) {
			t.Fatalf("pickLogo = %q", got)
		}
	})

	t.Run("takes an untagged logo when it is all there is", func(t *testing.T) {
		got := c.pickLogo([]rawImage{{FilePath: "/neutral.png"}})
		if got != c.imageURL("/neutral.png", c.images.logo) {
			t.Fatalf("pickLogo = %q", got)
		}
	})

	t.Run("no logos is empty, not a broken URL", func(t *testing.T) {
		if got := c.pickLogo(nil); got != "" {
			t.Fatalf("pickLogo(nil) = %q", got)
		}
		if got := c.pickLogo([]rawImage{{FilePath: ""}}); got != "" {
			t.Fatalf("pickLogo of an empty path = %q", got)
		}
	})
}

// The security-relevant one. A discover query is free text a user types into a
// settings screen and it is appended to a request that carries the credential.
func TestSanitiseDiscoverQueryDropsReservedParameters(t *testing.T) {
	// api_key is the one that matters: without this, a query could replace the
	// credential the module sends, silently, because url.Values.Set is
	// last-writer-wins and the substitution happens before the request is built.
	got := sanitiseDiscoverQuery("with_genres=53&api_key=attacker&page=9&language=xx&include_adult=true")
	parsed := mustParseQuery(t, got)

	for _, reserved := range []string{"api_key", "page", "language", "include_adult"} {
		if parsed.Has(reserved) {
			t.Errorf("sanitised query still carries %q: %q", reserved, got)
		}
	}
	if parsed.Get("with_genres") != "53" {
		t.Errorf("sanitised query lost the user's own filter: %q", got)
	}

	// Case is not a way around it.
	if parsed := mustParseQuery(t, sanitiseDiscoverQuery("API_KEY=attacker&with_genres=1")); parsed.Has("API_KEY") {
		t.Error("an upper-case reserved parameter survived")
	}

	// A leading "?" is what someone pastes from a URL bar.
	if parsed := mustParseQuery(t, sanitiseDiscoverQuery("?with_genres=27")); parsed.Get("with_genres") != "27" {
		t.Error("a query pasted with its leading ? was not accepted")
	}

	// Unparseable is empty, which drops the catalog rather than sending garbage.
	if got := sanitiseDiscoverQuery("%zz"); got != "" {
		t.Errorf("an unparseable query returned %q, want empty", got)
	}
}

func TestNormaliseCatalogSplitsTheEnteredPair(t *testing.T) {
	// The settings screen submits "name | query" as one value, because a
	// SubmitField submits on its own.
	got := normaliseCatalog(customCatalog{Name: " French Thrillers | with_genres=53&with_original_language=fr ", Type: typeMovie})
	if got.Name != "French Thrillers" || got.Query != "with_genres=53&with_original_language=fr" {
		t.Fatalf("normaliseCatalog = %+v", got)
	}

	// Idempotent: once split, the stored form round-trips unchanged.
	again := normaliseCatalog(got)
	if again != got {
		t.Fatalf("normaliseCatalog is not idempotent: %+v then %+v", got, again)
	}

	// An unknown type defaults to film rather than producing a catalog that
	// addresses no endpoint.
	if got := normaliseCatalog(customCatalog{Name: "x", Query: "y", Type: "nonsense"}); got.Type != typeMovie {
		t.Fatalf("type = %q, want the movie default", got.Type)
	}
	if got := normaliseCatalog(customCatalog{Name: "x", Query: "y", Type: typeTV}); got.Type != typeTV {
		t.Fatalf("type = %q, want tv preserved", got.Type)
	}
}

func TestCatalogsForAppendsCustomAndDropsUnusable(t *testing.T) {
	builtin := len(builtinCatalogs())
	got := catalogsFor([]customCatalog{
		{Name: "Good", Type: typeMovie, Query: "with_genres=53"},
		{Name: "", Type: typeMovie, Query: "with_genres=1"},        // no name
		{Name: "No query", Type: typeMovie, Query: ""},             // no query
		{Name: "Reserved only", Type: typeMovie, Query: "page=2"},  // nothing survives sanitising
		{Name: "Series", Type: typeTV, Query: "with_genres=10765"}, // kept
	})

	if len(got) != builtin+2 {
		t.Fatalf("got %d catalogs, want %d built-in plus 2 usable custom", len(got), builtin)
	}

	custom := got[builtin:]
	if !custom[0].Custom() || custom[0].Name != "Good" || custom[0].path != "/discover/movie" {
		t.Fatalf("first custom = %+v", custom[0])
	}
	if custom[1].path != "/discover/tv" {
		t.Fatalf("series catalog path = %q", custom[1].path)
	}
	// Ids must be distinct and stable, since the Platform addresses a catalog by
	// id and two may share a name.
	if custom[0].ID == custom[1].ID {
		t.Fatalf("custom catalogs share id %q", custom[0].ID)
	}
	for _, b := range got[:builtin] {
		if b.Custom() {
			t.Errorf("built-in catalog %q reports as custom", b.ID)
		}
	}
}

func TestKeywordsReadBothTMDBSpellings(t *testing.T) {
	// TMDB spells the same list `keywords` on a film and `results` on a series.
	// Decoding one and not the other is a silently empty list.
	film := rawTitle{}
	film.Keywords.Movie = []rawKeyword{{Name: "dystopia"}, {Name: " "}}
	if got := keywordsOf(film); !reflect.DeepEqual(got, []string{"dystopia"}) {
		t.Fatalf("film keywords = %v", got)
	}

	series := rawTitle{}
	series.Keywords.Series = []rawKeyword{{Name: "time loop"}}
	if got := keywordsOf(series); !reflect.DeepEqual(got, []string{"time loop"}) {
		t.Fatalf("series keywords = %v", got)
	}

	if got := keywordsOf(rawTitle{}); got != nil {
		t.Fatalf("no keywords = %v, want nil", got)
	}
}

func TestCertificationIsRegionExactOrEmpty(t *testing.T) {
	film := rawTitle{}
	film.ReleaseDates.Results = []rawReleaseDates{
		{CountryCode: "US", ReleaseDates: []struct {
			Certification string `json:"certification"`
		}{{Certification: "R"}}},
		{CountryCode: "GB", ReleaseDates: []struct {
			Certification string `json:"certification"`
		}{{Certification: ""}, {Certification: "15"}}},
	}

	gb := &Client{region: "GB", images: defaultImageConfig}
	if got := gb.certificationOf(film, typeMovie); got != "15" {
		t.Fatalf("GB certification = %q, want 15 (skipping the release with none)", got)
	}

	// A region TMDB has no rating for is empty — *not* another country's rating.
	// A US "R" shown to a household that set DE is a different scale reported as
	// if it were theirs.
	de := &Client{region: "DE", images: defaultImageConfig}
	if got := de.certificationOf(film, typeMovie); got != "" {
		t.Fatalf("DE certification = %q, want empty rather than a substitute", got)
	}

	// No region configured means no claim.
	none := &Client{images: defaultImageConfig}
	if got := none.certificationOf(film, typeMovie); got != "" {
		t.Fatalf("unset region gave %q", got)
	}

	series := rawTitle{}
	series.ContentRatings.Results = []rawContentRating{{CountryCode: "GB", Rating: "15"}}
	if got := gb.certificationOf(series, typeTV); got != "15" {
		t.Fatalf("series certification = %q", got)
	}
}

func TestTrailersDropNonTrailersAndRankOfficialFirst(t *testing.T) {
	got := trailersOf([]rawVideo{
		{Name: "Behind the scenes", Site: "YouTube", Key: "a", Type: "Featurette"},
		{Name: "Fan cut", Site: "YouTube", Key: "b", Type: "Trailer"},
		{Name: "Official Trailer", Site: "YouTube", Key: "c", Type: "Trailer", Official: true},
		{Name: "Teaser", Site: "YouTube", Key: "d", Type: "Teaser"},
		{Name: "No key", Site: "YouTube", Key: "", Type: "Trailer"},
	})

	if len(got) != 3 {
		t.Fatalf("got %d trailers, want 3 (featurette and keyless dropped): %+v", len(got), got)
	}
	if !got[0].Official || got[0].Key != "c" {
		t.Fatalf("first trailer = %+v, want the official one", got[0])
	}
	// A site and a key, never a URL: building one is an embed-policy decision
	// that belongs to the client.
	for _, tr := range got {
		if tr.Site == "" || tr.Key == "" {
			t.Errorf("trailer %+v is missing a site or key", tr)
		}
	}
}

func TestIsIMDbID(t *testing.T) {
	// IMDb ids are "tt" plus digits, TMDB's are bare integers, so the two are
	// unambiguous — which is what lets a ref from another module be recognised.
	for _, id := range []string{"tt1856101", "tt0903747"} {
		if !isIMDbID(id) {
			t.Errorf("%q not recognised as an IMDb id", id)
		}
	}
	for _, id := range []string{"335984", "", "tt", "1396"} {
		if isIMDbID(id) {
			t.Errorf("%q wrongly treated as an IMDb id", id)
		}
	}
}

func TestAppendForDiffersByType(t *testing.T) {
	// A film's age rating lives in release_dates, a series' in content_ratings.
	// Appending the wrong one returns nothing and the certification is silently
	// empty.
	if got := appendFor(typeMovie); !contains(got, "release_dates") || contains(got, "content_ratings") {
		t.Fatalf("film append list = %q", got)
	}
	if got := appendFor(typeTV); !contains(got, "content_ratings") || contains(got, "release_dates") {
		t.Fatalf("series append list = %q", got)
	}
	for _, want := range []string{"credits", "images", "external_ids", "keywords", "recommendations", "videos"} {
		if !contains(appendFor(typeMovie), want) {
			t.Errorf("film append list is missing %q", want)
		}
	}
}

func TestSettingsFromAppliesDefaultsAndNormalises(t *testing.T) {
	s, err := settingsFrom([]byte(`{"apiKey":" key ","region":"gb"}`))
	if err != nil {
		t.Fatalf("settingsFrom: %v", err)
	}
	if s.APIKey != "key" {
		t.Errorf("apiKey = %q, want it trimmed — a pasted key carries whitespace", s.APIKey)
	}
	if s.Region != "GB" {
		t.Errorf("region = %q, want it upper-cased", s.Region)
	}
	if s.Language != defaultLanguage {
		t.Errorf("language = %q, want the default", s.Language)
	}

	empty, err := settingsFrom(nil)
	if err != nil {
		t.Fatalf("settingsFrom(nil): %v", err)
	}
	if empty.Language != defaultLanguage || empty.APIKey != "" {
		t.Fatalf("empty settings = %+v", empty)
	}
}

func TestGroupBySeasonPreservesOrder(t *testing.T) {
	groups := groupBySeason([]Episode{
		{Season: 0, Episode: 1}, {Season: 1, Episode: 1}, {Season: 1, Episode: 2}, {Season: 2, Episode: 1},
	})
	if len(groups) != 3 {
		t.Fatalf("groups = %d, want 3", len(groups))
	}
	if groups[0].number != 0 || len(groups[1].episodes) != 2 {
		t.Fatalf("groups = %+v", groups)
	}
	if groupBySeason(nil) != nil {
		t.Fatal("no episodes must group to nothing")
	}
}

func TestSeasonTitleNamesSpecials(t *testing.T) {
	if got := seasonTitle(0); got != "Specials" {
		t.Fatalf("seasonTitle(0) = %q; TMDB numbers specials 0 and 'Season 0' reads as a bug", got)
	}
	if got := seasonTitle(3); got != "Season 3" {
		t.Fatalf("seasonTitle(3) = %q", got)
	}
}

func TestMaskKey(t *testing.T) {
	if got := maskKey("0123456789abcdef"); got != "••••cdef" {
		t.Fatalf("maskKey = %q, want the last four only", got)
	}
	// A short value must not leak most of itself by being "mostly shown".
	if got := maskKey("abc"); got != "••••" {
		t.Fatalf("maskKey of a short value = %q", got)
	}
}

func TestRetryAfterIsClamped(t *testing.T) {
	// A hostile or absent value must not stall a request for minutes.
	if got := retryAfter("3600").Seconds(); got != 10 {
		t.Errorf("retryAfter(3600) = %vs, want the 10s ceiling", got)
	}
	if got := retryAfter("").Seconds(); got != 1 {
		t.Errorf("retryAfter(\"\") = %vs, want the 1s floor", got)
	}
	if got := retryAfter("2").Seconds(); got != 2 {
		t.Errorf("retryAfter(2) = %vs", got)
	}
}

// contains is substring containment, for asserting on the append list.
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// mustParseQuery decodes a sanitised query so a test can assert per parameter
// rather than on the encoded string, whose ordering is not guaranteed.
func mustParseQuery(t *testing.T, query string) url.Values {
	t.Helper()
	parsed, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}
	return parsed
}

// The bundled read access token. These live in the internal test package
// because defaultReadAccessToken is set by the linker and is deliberately
// unexported — nothing outside this package can read it, which is half the
// point.

// withBundledToken sets the linked-in token for one test and restores it.
func withBundledToken(t *testing.T, token string) {
	t.Helper()
	previous := defaultReadAccessToken
	defaultReadAccessToken = token
	t.Cleanup(func() { defaultReadAccessToken = previous })
}

func TestResolveTokenPrefersTheUsersOwn(t *testing.T) {
	withBundledToken(t, "bundled.jwt.value")

	// A user's key wins immediately — no flag, no opt-out, no restart.
	token, bundled, ok := resolveToken(settings{APIKey: "mine"})
	if !ok || bundled || token != "mine" {
		t.Fatalf("resolveToken(user) = %q bundled=%v ok=%v", token, bundled, ok)
	}

	// With none of their own, the bundled one is used and reported as bundled,
	// so a screen can say which without holding the value.
	token, bundled, ok = resolveToken(settings{})
	if !ok || !bundled || token != "bundled.jwt.value" {
		t.Fatalf("resolveToken(none) = %q bundled=%v ok=%v", token, bundled, ok)
	}
}

func TestResolveTokenWithNothingLinkedIn(t *testing.T) {
	withBundledToken(t, "")

	if _, _, ok := resolveToken(settings{}); ok {
		t.Fatal("resolved a token with neither a user key nor a bundled one")
	}
	// An ordinary `go build` links nothing in, which is the state every test
	// above this one runs in.
	if token, _, ok := resolveToken(settings{APIKey: "mine"}); !ok || token != "mine" {
		t.Fatalf("a user key must still work with no bundled token: %q", token)
	}
}

// The requirement that matters: the bundled token is never rendered and never
// written into the settings document a client round-trips.
func TestSettingsUINeverEmitsTheBundledToken(t *testing.T) {
	const bundled = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJib3VuZGxlZCJ9.s3cr3t-signature"
	withBundledToken(t, bundled)
	capability := New(nil)

	for _, document := range [][]byte{nil, []byte(`{}`), []byte(`{"apiKey":"user-key-0123"}`)} {
		resp, err := capability.SettingsUI(context.Background(), v1.SettingsUIRequest{
			Caller: v1.CallerFromSession("s-1"), Settings: document,
		})
		if err != nil {
			t.Fatalf("SettingsUI(%s): %v", document, err)
		}
		screen := string(resp.UI)
		if strings.Contains(screen, bundled) {
			t.Fatalf("the bundled token appears in the settings screen for settings %s", document)
		}
		// Not even its signature segment, which is the part that would survive a
		// careless "show the last few characters" helper.
		if strings.Contains(screen, "s3cr3t-signature") {
			t.Fatalf("part of the bundled token leaked for settings %s", document)
		}
	}
}

// configureModule replaces the whole settings document, so every control on the
// screen carries one. If the bundled token ever reached settings.APIKey it would
// be written into the user's stored settings by the next control they touched —
// silently turning a shared build-time credential into their configuration.
func TestTheBundledTokenNeverEntersTheSettingsDocument(t *testing.T) {
	const bundled = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJib3VuZGxlZCJ9.signature"
	withBundledToken(t, bundled)

	parsed, err := settingsFrom([]byte(`{"region":"GB"}`))
	if err != nil {
		t.Fatalf("settingsFrom: %v", err)
	}
	if parsed.APIKey != "" {
		t.Fatalf("settings.APIKey = %q; it must only ever hold the user's own key", parsed.APIKey)
	}

	// The action payload every control emits.
	input := configureInput(parsed)
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), bundled) {
		t.Fatalf("the bundled token is in a configureModule payload: %s", encoded)
	}
	settingsDoc, _ := input["settings"].(map[string]any)
	if got := settingsDoc["apiKey"]; got != "" {
		t.Fatalf("apiKey in the emitted document = %v, want empty", got)
	}
}

func TestSettingsUISaysWhichKeyIsInUse(t *testing.T) {
	capability := New(nil)
	render := func(t *testing.T, document []byte) string {
		t.Helper()
		resp, err := capability.SettingsUI(context.Background(), v1.SettingsUIRequest{
			Caller: v1.CallerFromSession("s-1"), Settings: document,
		})
		if err != nil {
			t.Fatalf("SettingsUI: %v", err)
		}
		return string(resp.UI)
	}

	t.Run("bundled key, none of the user's own", func(t *testing.T) {
		withBundledToken(t, "eyJhbGciOiJIUzI1NiJ9.eyJhIjoxfQ.sig")
		screen := render(t, nil)
		// A user with working metadata and no key of their own must not see a
		// screen that reads as broken.
		if !strings.Contains(screen, "Bundled key in use") {
			t.Fatalf("no indication the bundled key is in use: %s", screen)
		}
		if strings.Contains(screen, "do nothing until a key is set") {
			t.Fatal("the screen warns metadata is dead while the bundled key is working")
		}
	})

	t.Run("no key at all", func(t *testing.T) {
		withBundledToken(t, "")
		screen := render(t, nil)
		if !strings.Contains(screen, "do nothing until a key is set") {
			t.Fatalf("no warning with no credential at all: %s", screen)
		}
	})

	t.Run("the user's own key", func(t *testing.T) {
		withBundledToken(t, "eyJhbGciOiJIUzI1NiJ9.eyJhIjoxfQ.sig")
		screen := render(t, []byte(`{"apiKey":"0123456789abcdef0123456789abcdef"}`))
		if !strings.Contains(screen, "Your own key is in use") {
			t.Fatalf("does not say the user's key won: %s", screen)
		}
		// Clearing it is not destructive when there is something to fall back to,
		// and the screen should say so rather than implying it breaks metadata.
		if !strings.Contains(screen, "falls back to the key bundled with Mosaic") {
			t.Fatalf("does not explain what clearing does: %s", screen)
		}
	})
}
