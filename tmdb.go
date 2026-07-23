package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The TMDB v3 HTTP client. This file is the anti-corruption layer (ADR 0051):
// every TMDB-ism — its two auth schemes, its split of one logical record across
// `append_to_response` sub-objects, its image paths that are not URLs, its
// per-season episode endpoint — stops here. Nothing above it sees a TMDB shape.

const (
	// apiBase is TMDB's v3 API root. There is no other host and no versioned
	// negotiation: v3 is the current API and v4 is an additive auth scheme over
	// the same endpoints, which is why the token below can be either.
	apiBase = "https://api.themoviedb.org/3"
	// imageBase is TMDB's CDN root. A TMDB record carries a *path*
	// ("/xyz.jpg"), never a URL — the size is the caller's choice, so the URL
	// does not exist until this module builds it.
	imageBase = "https://image.tmdb.org/t/p/"
	// userAgent identifies Mosaic to TMDB. Sent for the same reason the Stremio
	// module sends one: an unnamed client is the one that gets rate-limited or
	// refused first, and the failure reads as the API being down.
	userAgent = "Mosaic/1.0 (+https://github.com/mosaic-media)"
)

// Image sizes. TMDB serves each asset at a fixed set of widths and the caller
// picks one per use; these are the sizes each surface actually renders at, so a
// card does not download a 2000px poster to draw it 200px wide.
const (
	posterSize   = "w500"
	backdropSize = "w1280"
	logoSize     = "w500"
	profileSize  = "w185"
	stillSize    = "w300"
)

// seasonFetchConcurrency bounds the per-season episode fetches a series detail
// needs. TMDB has no endpoint returning every episode of a show, so a series
// costs one request per season — a long-running soap is dozens. Serial is too
// slow for a detail screen and unbounded is a burst TMDB will rate-limit, so
// this is the middle.
const seasonFetchConcurrency = 6

// Client is a configured TMDB API client. It is built per invocation from the
// module's settings rather than held on the Capability, because the API key is
// user-managed configuration the Platform hands in on each call (ADR 0021) and
// may change between two of them.
type Client struct {
	http     *http.Client
	token    string
	bearer   bool
	language string
	region   string
	adult    bool
}

// NewClient builds a client over an HTTP client and a resolved settings value.
// The Platform's own client is passed in rather than built here: it carries the
// netguard dial guard and the outbound telemetry seam (ADR 0055).
func NewClient(httpClient *http.Client, s settings) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{
		http:  httpClient,
		token: s.APIKey,
		// TMDB has two credentials for the same endpoints: a v3 API key (a bare
		// hex string, sent as a query parameter) and a v4 read access token (a
		// JWT, sent as a bearer header). A user copying from TMDB's settings page
		// may arrive with either and has no reason to know which Mosaic wants, so
		// the shape decides rather than the user.
		bearer:   isBearerToken(s.APIKey),
		language: s.Language,
		region:   s.Region,
		adult:    s.IncludeAdult,
	}
}

// isBearerToken reports whether a credential is a v4 read access token rather
// than a v3 API key. A JWT has three dot-separated segments; a v3 key is 32 hex
// characters and has none.
func isBearerToken(token string) bool {
	return strings.Count(token, ".") == 2
}

// get performs one GET against the API and decodes the JSON body into out.
//
// It handles the two failure shapes TMDB actually returns rather than treating
// any non-200 alike: an authentication failure (the overwhelmingly likely
// misconfiguration, since the module is useless without a key) is reported as
// such, and a 429 is retried once honouring Retry-After. Everything else
// carries TMDB's own status message, which is more useful than the code.
func (c *Client) get(ctx context.Context, path string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	if c.language != "" {
		params.Set("language", c.language)
	}
	if !c.bearer {
		params.Set("api_key", c.token)
	}

	endpoint := apiBase + path
	if encoded := params.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", userAgent)
		if c.bearer {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return fmt.Errorf("call TMDB %s: %w", path, err)
		}

		switch {
		case resp.StatusCode == http.StatusOK:
			err := json.NewDecoder(resp.Body).Decode(out)
			resp.Body.Close()
			if err != nil {
				return fmt.Errorf("decode TMDB %s: %w", path, err)
			}
			return nil

		case resp.StatusCode == http.StatusTooManyRequests && attempt == 0:
			wait := retryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue

		case resp.StatusCode == http.StatusUnauthorized:
			resp.Body.Close()
			return errNoAPIKey

		default:
			msg := statusMessage(resp)
			resp.Body.Close()
			return fmt.Errorf("TMDB %s returned %d: %s", path, resp.StatusCode, msg)
		}
	}
}

// retryAfter reads a Retry-After header in seconds, clamped so a hostile or
// absent value cannot stall a request for minutes.
func retryAfter(header string) time.Duration {
	seconds, err := strconv.Atoi(strings.TrimSpace(header))
	if err != nil || seconds < 1 {
		seconds = 1
	}
	if seconds > 10 {
		seconds = 10
	}
	return time.Duration(seconds) * time.Second
}

// statusMessage extracts TMDB's own error text from a failed response, falling
// back to the HTTP status when the body is not the error shape.
func statusMessage(resp *http.Response) string {
	var body struct {
		StatusMessage string `json:"status_message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.StatusMessage != "" {
		return body.StatusMessage
	}
	return resp.Status
}

// Search queries TMDB's multi-search and returns film and television matches in
// TMDB's own relevance order. People are dropped: they are not content, and
// nothing downstream could materialise one.
func (c *Client) Search(ctx context.Context, text string) ([]Preview, error) {
	params := url.Values{}
	params.Set("query", text)
	params.Set("include_adult", strconv.FormatBool(c.adult))
	params.Set("page", "1")

	var resp struct {
		Results []rawPreview `json:"results"`
	}
	if err := c.get(ctx, "/search/multi", params, &resp); err != nil {
		return nil, err
	}

	out := make([]Preview, 0, len(resp.Results))
	for _, r := range resp.Results {
		if r.MediaType != typeMovie && r.MediaType != typeTV {
			continue
		}
		out = append(out, r.preview(r.MediaType))
	}
	return out, nil
}

// Catalogs is the fixed set of collections this module exposes. Unlike a Stremio
// addon, which declares its catalogs in a manifest, TMDB has no such
// declaration — it has endpoints — so the list is curated here. It is
// deliberately short: these are the views a home screen renders, not every
// discover query TMDB can answer.
func (c *Client) Catalogs() []CatalogDecl {
	return []CatalogDecl{
		{ID: "trending", Type: typeMovie, Name: "Trending Films", path: "/trending/movie/week"},
		{ID: "trending", Type: typeTV, Name: "Trending Series", path: "/trending/tv/week"},
		{ID: "popular", Type: typeMovie, Name: "Popular Films", path: "/movie/popular"},
		{ID: "popular", Type: typeTV, Name: "Popular Series", path: "/tv/popular"},
		{ID: "top_rated", Type: typeMovie, Name: "Top Rated Films", path: "/movie/top_rated"},
		{ID: "top_rated", Type: typeTV, Name: "Top Rated Series", path: "/tv/top_rated"},
		{ID: "now_playing", Type: typeMovie, Name: "In Cinemas", path: "/movie/now_playing"},
		{ID: "on_the_air", Type: typeTV, Name: "On The Air", path: "/tv/on_the_air"},
	}
}

// catalogPage is how many items one TMDB list page holds. It is fixed by the
// API, and it is what converts the Platform's item-offset Skip into a page
// number.
const catalogPage = 20

// CatalogItems lists one catalog's entries. Skip is an item offset and TMDB
// pages in twenties, so it is converted rather than passed through; an offset
// that lands mid-page rounds down, which repeats at most nineteen items rather
// than skipping any.
func (c *Client) CatalogItems(ctx context.Context, catalogID, nativeType string, skip int) ([]Preview, error) {
	decl, ok := c.findCatalog(catalogID, nativeType)
	if !ok {
		return nil, fmt.Errorf("unknown catalog %q for type %q", catalogID, nativeType)
	}

	params := url.Values{}
	params.Set("page", strconv.Itoa(skip/catalogPage+1))
	if c.region != "" {
		params.Set("region", c.region)
	}

	var resp struct {
		Results []rawPreview `json:"results"`
	}
	if err := c.get(ctx, decl.path, params, &resp); err != nil {
		return nil, err
	}

	out := make([]Preview, 0, len(resp.Results))
	for _, r := range resp.Results {
		// A list endpoint's results carry no media_type — the endpoint *is* the
		// type — so it comes from the catalog rather than from the record. The
		// trending endpoints do return one; taking the declaration's either way
		// keeps a single path.
		out = append(out, r.preview(decl.Type))
	}
	return out, nil
}

// findCatalog resolves a catalog declaration by its id and type. Two catalogs
// share an id ("popular" for film and for television), so the type is part of
// the key rather than decoration.
func (c *Client) findCatalog(id, nativeType string) (CatalogDecl, bool) {
	for _, decl := range c.Catalogs() {
		if decl.ID == id && decl.Type == nativeType {
			return decl, true
		}
	}
	return CatalogDecl{}, false
}

// Detail fetches one title's full record: the descriptive fields, its billed
// cast, its artwork variants and its external ids, in **one** request.
//
// The single request is `append_to_response`, and it is worth naming because the
// obvious implementation is four. TMDB splits credits, images and external ids
// onto their own endpoints; appending them folds all four into one round trip,
// which for a detail screen is the difference between one latency and four.
//
// For a series it then fetches each season's episodes, which TMDB offers no way
// to avoid — there is no endpoint that returns a show's whole episode list.
func (c *Client) Detail(ctx context.Context, nativeType, id string) (Title, error) {
	if nativeType != typeMovie && nativeType != typeTV {
		return Title{}, fmt.Errorf("unsupported TMDB type %q; expected %q or %q", nativeType, typeMovie, typeTV)
	}

	params := url.Values{}
	params.Set("append_to_response", "credits,images,external_ids")
	// Without this, `images` returns only assets tagged with the request
	// language and a show whose logo is untagged appears to have none. The
	// explicit null is TMDB's spelling of "language-neutral", which is where most
	// clearlogos actually live.
	params.Set("include_image_language", imageLanguages(c.language))

	var raw rawTitle
	if err := c.get(ctx, "/"+nativeType+"/"+id, params, &raw); err != nil {
		return Title{}, err
	}

	title := raw.title(nativeType)
	if nativeType == typeTV {
		episodes, err := c.episodes(ctx, id, raw.Seasons)
		if err != nil {
			return Title{}, err
		}
		title.Episodes = episodes
	}
	return title, nil
}

// imageLanguages builds the include_image_language value: the configured
// language's base code, English as a fallback, and language-neutral assets.
func imageLanguages(language string) string {
	base := "en"
	if code, _, ok := strings.Cut(language, "-"); ok && code != "" {
		base = code
	}
	if base == "en" {
		return "en,null"
	}
	return base + ",en,null"
}

// episodes fetches every season's episode list concurrently, bounded, and
// returns them flattened in season/episode order.
//
// A season that fails is dropped rather than failing the title. A detail screen
// missing one season of episodes is a visible gap a user can act on; a detail
// screen that will not render because season 7 of 12 timed out is not.
func (c *Client) episodes(ctx context.Context, id string, seasons []rawSeasonSummary) ([]Episode, error) {
	if len(seasons) == 0 {
		return nil, nil
	}

	var (
		mu   sync.Mutex
		out  []Episode
		wg   sync.WaitGroup
		slot = make(chan struct{}, seasonFetchConcurrency)
	)

	for _, s := range seasons {
		if s.EpisodeCount == 0 {
			continue
		}
		wg.Add(1)
		go func(number int) {
			defer wg.Done()
			slot <- struct{}{}
			defer func() { <-slot }()

			var resp struct {
				Episodes []rawEpisode `json:"episodes"`
			}
			if err := c.get(ctx, "/tv/"+id+"/season/"+strconv.Itoa(number), nil, &resp); err != nil {
				return
			}
			mu.Lock()
			for _, e := range resp.Episodes {
				out = append(out, e.episode(number))
			}
			mu.Unlock()
		}(s.SeasonNumber)
	}
	wg.Wait()

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Episode < out[j].Episode
	})
	return out, nil
}

// The translated types. Everything above the client speaks these; nothing above
// it speaks TMDB's.

// Preview is one search or catalog result — enough to render a row and to
// address the title later.
type Preview struct {
	ID         string
	NativeType string
	Title      string
	Year       int
	Poster     string
}

// CatalogDecl is one collection this module exposes. The path is unexported:
// which endpoint backs a catalog is this file's business, not a caller's.
type CatalogDecl struct {
	ID   string
	Type string
	Name string
	path string
}

// Title is one film or series, fully described.
type Title struct {
	ID         string
	NativeType string
	IMDbID     string
	Title      string
	Year       int
	Overview   string
	Poster     string
	Backdrop   string
	Logo       string
	Genres     []string
	Rating     float64
	Runtime    string
	Cast       []Credit
	// Episodes is populated for a series only, in season/episode order.
	Episodes []Episode
	// CollectionName is the franchise a film belongs to ("The Matrix
	// Collection"), empty otherwise. Carried because TMDB has it and it is one
	// of the gaps a metadata module was meant to close — see the README for why
	// nothing consumes it yet.
	CollectionName string
}

// Credit is one billed cast member.
type Credit struct {
	Name      string
	Character string
	Photo     string
}

// Episode is one episode of a series.
type Episode struct {
	Season    int
	Episode   int
	Title     string
	Overview  string
	Thumbnail string
	Released  string
}

// The raw TMDB wire shapes. They exist only to be decoded and immediately
// translated; no other file references them.

const (
	typeMovie = "movie"
	typeTV    = "tv"
)

type rawPreview struct {
	ID           int    `json:"id"`
	MediaType    string `json:"media_type"`
	Title        string `json:"title"`
	Name         string `json:"name"`
	ReleaseDate  string `json:"release_date"`
	FirstAirDate string `json:"first_air_date"`
	PosterPath   string `json:"poster_path"`
}

// preview translates a wire result, resolving TMDB's parallel field pairs. A
// film has `title`/`release_date` and a series has `name`/`first_air_date` for
// the same two facts, which is the single most repetitive TMDB-ism and is
// collapsed here so nothing downstream branches on it.
func (r rawPreview) preview(nativeType string) Preview {
	title, date := r.Title, r.ReleaseDate
	if nativeType == typeTV {
		title, date = r.Name, r.FirstAirDate
	}
	return Preview{
		ID:         strconv.Itoa(r.ID),
		NativeType: nativeType,
		Title:      title,
		Year:       parseYear(date),
		Poster:     imageURL(r.PosterPath, posterSize),
	}
}

type rawTitle struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	Overview     string  `json:"overview"`
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
	PosterPath   string  `json:"poster_path"`
	BackdropPath string  `json:"backdrop_path"`
	VoteAverage  float64 `json:"vote_average"`
	Runtime      int     `json:"runtime"`
	// EpisodeRunTime is a *list* on a series, because episodes vary; the first
	// entry is TMDB's typical value.
	EpisodeRunTime []int `json:"episode_run_time"`
	Genres         []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Seasons             []rawSeasonSummary `json:"seasons"`
	BelongsToCollection *struct {
		Name string `json:"name"`
	} `json:"belongs_to_collection"`
	Credits struct {
		Cast []rawCast `json:"cast"`
	} `json:"credits"`
	Images struct {
		Logos []rawImage `json:"logos"`
	} `json:"images"`
	ExternalIDs struct {
		IMDbID string `json:"imdb_id"`
	} `json:"external_ids"`
}

type rawSeasonSummary struct {
	SeasonNumber int `json:"season_number"`
	EpisodeCount int `json:"episode_count"`
}

type rawCast struct {
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profile_path"`
	Order       int    `json:"order"`
}

type rawImage struct {
	FilePath    string  `json:"file_path"`
	ISO639      string  `json:"iso_639_1"`
	VoteAverage float64 `json:"vote_average"`
}

type rawEpisode struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	StillPath     string `json:"still_path"`
	AirDate       string `json:"air_date"`
}

func (r rawEpisode) episode(season int) Episode {
	return Episode{
		Season:    season,
		Episode:   r.EpisodeNumber,
		Title:     r.Name,
		Overview:  r.Overview,
		Thumbnail: imageURL(r.StillPath, stillSize),
		Released:  r.AirDate,
	}
}

// maxCast is how many billed cast members a detail carries. A detail screen
// shows the *top* cast; TMDB returns the entire credited ensemble, which for a
// large production is hundreds of people and megabytes of headshots.
const maxCast = 18

// title translates a full TMDB record into the module's own shape.
func (r rawTitle) title(nativeType string) Title {
	name, date := r.Title, r.ReleaseDate
	if nativeType == typeTV {
		name, date = r.Name, r.FirstAirDate
	}

	genres := make([]string, 0, len(r.Genres))
	for _, g := range r.Genres {
		genres = append(genres, g.Name)
	}

	// Billing order, explicitly. TMDB usually returns cast in `order` already,
	// but it is a field rather than a guarantee, and a cast rail whose lead is
	// fourth reads as broken.
	cast := append([]rawCast(nil), r.Credits.Cast...)
	sort.SliceStable(cast, func(i, j int) bool { return cast[i].Order < cast[j].Order })
	if len(cast) > maxCast {
		cast = cast[:maxCast]
	}
	credits := make([]Credit, 0, len(cast))
	for _, c := range cast {
		credits = append(credits, Credit{
			Name:      c.Name,
			Character: c.Character,
			Photo:     imageURL(c.ProfilePath, profileSize),
		})
	}

	out := Title{
		ID:         strconv.Itoa(r.ID),
		NativeType: nativeType,
		IMDbID:     strings.TrimSpace(r.ExternalIDs.IMDbID),
		Title:      name,
		Year:       parseYear(date),
		Overview:   r.Overview,
		Poster:     imageURL(r.PosterPath, posterSize),
		Backdrop:   imageURL(r.BackdropPath, backdropSize),
		Logo:       pickLogo(r.Images.Logos),
		Genres:     genres,
		Rating:     r.VoteAverage,
		Runtime:    runtimeLabel(r.Runtime, r.EpisodeRunTime),
		Cast:       credits,
	}
	if r.BelongsToCollection != nil {
		out.CollectionName = r.BelongsToCollection.Name
	}
	return out
}

// pickLogo chooses one clearlogo from the variants TMDB returns, preferring the
// best-voted asset that carries a language over an untagged one.
//
// The preference is the opposite of the intuitive order, and deliberately: an
// untagged logo is usually the plain wordmark with no localisation, while a
// tagged one has been vetted as the title treatment for a language. The request
// already restricted the set to the languages that are acceptable, so anything
// returned is a legitimate choice and this is only ranking within it.
func pickLogo(logos []rawImage) string {
	best := -1
	for i, l := range logos {
		if l.FilePath == "" {
			continue
		}
		if best < 0 {
			best = i
			continue
		}
		current, candidate := logos[best], l
		switch {
		case current.ISO639 == "" && candidate.ISO639 != "":
			best = i
		case (current.ISO639 == "") == (candidate.ISO639 == "") && candidate.VoteAverage > current.VoteAverage:
			best = i
		}
	}
	if best < 0 {
		return ""
	}
	return imageURL(logos[best].FilePath, logoSize)
}

// runtimeLabel renders a runtime as the display string the SDK asks for. The
// contract is explicit that this is display-only text rather than a duration
// (ADR 0034), because sources disagree on the format — so the module picks one
// and the Platform never parses it back.
func runtimeLabel(movieRuntime int, episodeRuntime []int) string {
	minutes := movieRuntime
	if minutes == 0 && len(episodeRuntime) > 0 {
		minutes = episodeRuntime[0]
	}
	if minutes <= 0 {
		return ""
	}
	if minutes < 60 {
		return strconv.Itoa(minutes) + " min"
	}
	hours, remainder := minutes/60, minutes%60
	if remainder == 0 {
		return strconv.Itoa(hours) + "h"
	}
	return strconv.Itoa(hours) + "h " + strconv.Itoa(remainder) + "m"
}

// imageURL turns a TMDB image path into a URL at the given size. An empty path
// yields an empty URL rather than a link to nothing, so "the source had none"
// stays distinguishable from a broken image.
func imageURL(path, size string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return imageBase + size + path
}

// parseYear reads the leading year from a TMDB date ("2017-04-21"), returning 0
// when there is none. TMDB omits the field entirely for an unannounced title, so
// absent is normal rather than an error.
func parseYear(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	year, err := strconv.Atoi(date[:4])
	if err != nil {
		return 0
	}
	return year
}
