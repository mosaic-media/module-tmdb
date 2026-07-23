package tmdb

import "testing"

// Internal tests for the translations that have no observable surface of their
// own but decide what every role returns.

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
	if got := imageURL("/abc.jpg", posterSize); got != imageBase+posterSize+"/abc.jpg" {
		t.Fatalf("imageURL = %q", got)
	}
	// An absent asset must stay absent. A URL to nothing renders as a broken
	// image, which is worse than a fallback.
	for _, path := range []string{"", "   "} {
		if got := imageURL(path, posterSize); got != "" {
			t.Fatalf("imageURL(%q) = %q, want empty", path, got)
		}
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
	t.Run("prefers a language-tagged logo over an untagged one", func(t *testing.T) {
		got := pickLogo([]rawImage{
			{FilePath: "/neutral.png", VoteAverage: 9},
			{FilePath: "/english.png", ISO639: "en", VoteAverage: 1},
		})
		if got != imageURL("/english.png", logoSize) {
			t.Fatalf("pickLogo = %q", got)
		}
	})

	t.Run("breaks a tie on votes", func(t *testing.T) {
		got := pickLogo([]rawImage{
			{FilePath: "/low.png", ISO639: "en", VoteAverage: 2},
			{FilePath: "/high.png", ISO639: "en", VoteAverage: 8},
		})
		if got != imageURL("/high.png", logoSize) {
			t.Fatalf("pickLogo = %q", got)
		}
	})

	t.Run("takes an untagged logo when it is all there is", func(t *testing.T) {
		got := pickLogo([]rawImage{{FilePath: "/neutral.png"}})
		if got != imageURL("/neutral.png", logoSize) {
			t.Fatalf("pickLogo = %q", got)
		}
	})

	t.Run("no logos is empty, not a broken URL", func(t *testing.T) {
		if got := pickLogo(nil); got != "" {
			t.Fatalf("pickLogo(nil) = %q", got)
		}
		if got := pickLogo([]rawImage{{FilePath: ""}}); got != "" {
			t.Fatalf("pickLogo of an empty path = %q", got)
		}
	})
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
