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
// per-season episode endpoint, its parallel field names for films and series —
// stops here. Nothing above it sees a TMDB shape.

const (
	// apiBase is TMDB's v3 API root. There is no other host and no versioned
	// negotiation: v3 is the current API and v4 is an additive auth scheme over
	// the same endpoints, which is why the token below can be either.
	apiBase = "https://api.themoviedb.org/3"
	// userAgent identifies Mosaic to TMDB. Sent for the same reason the Stremio
	// module sends one: an unnamed client is the one that gets rate-limited or
	// refused first, and the failure reads as the API being down.
	userAgent = "Mosaic/1.0 (+https://github.com/mosaic-media)"
)

// seasonFetchConcurrency bounds the per-season episode fetches a series detail
// needs. TMDB has no endpoint returning every episode of a show, so a series
// costs one request per season — a long-running soap is dozens. Serial is too
// slow for a detail screen and unbounded is a burst TMDB will rate-limit, so
// this is the middle.
const seasonFetchConcurrency = 6

// maxSimilar bounds the related-titles list. TMDB returns twenty per page; a
// detail screen shows a rail, not a catalogue.
const maxSimilar = 12

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
	catalogs []CatalogDecl
	images   imageConfig
}

// NewClient builds a client over an HTTP client and a resolved settings value.
// The Platform's own client is passed in rather than built here: it carries the
// netguard dial guard and the outbound telemetry seam (ADR 0055).
func NewClient(httpClient *http.Client, s settings, images imageConfig) *Client {
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
		catalogs: catalogsFor(s.Catalogs),
		images:   images,
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
		out = append(out, c.preview(r, r.MediaType))
	}
	return out, nil
}

// FindByIMDb resolves an IMDb id to this source's own id — the reverse of the
// lookup every other call makes.
//
// It is what lets TMDB describe a title someone else materialised. Cinemeta and
// every Stremio addon key on IMDb ids (ADR 0072 makes a credential-free,
// IMDb-keyed source the guaranteed floor), so without this the richer provider
// could not answer for a single work in such a library — it would hold no id it
// recognised. Returns false, no error, when TMDB knows the id but has no film or
// series behind it.
func (c *Client) FindByIMDb(ctx context.Context, imdbID string) (string, string, bool, error) {
	params := url.Values{}
	params.Set("external_source", "imdb_id")

	var resp struct {
		MovieResults []rawPreview `json:"movie_results"`
		TVResults    []rawPreview `json:"tv_results"`
		// An IMDb id can name an episode, which resolves to its show rather than
		// to nothing — the show is what a detail screen wants.
		EpisodeResults []struct {
			ShowID int `json:"show_id"`
		} `json:"tv_episode_results"`
	}
	if err := c.get(ctx, "/find/"+url.PathEscape(imdbID), params, &resp); err != nil {
		return "", "", false, err
	}

	switch {
	case len(resp.MovieResults) > 0:
		return strconv.Itoa(resp.MovieResults[0].ID), typeMovie, true, nil
	case len(resp.TVResults) > 0:
		return strconv.Itoa(resp.TVResults[0].ID), typeTV, true, nil
	case len(resp.EpisodeResults) > 0:
		return strconv.Itoa(resp.EpisodeResults[0].ShowID), typeTV, true, nil
	default:
		return "", "", false, nil
	}
}

// Detail fetches one title's full record in **one** request.
//
// The single request is `append_to_response`, and it is worth naming because the
// obvious implementation is eight. TMDB splits credits, images, external ids,
// keywords, recommendations, videos and certifications onto their own endpoints;
// appending folds all of them into one round trip, which for a detail screen is
// the difference between one latency and eight. TMDB allows twenty appended
// sub-requests, so this is well inside the budget.
//
// Two things it cannot fold in, and both are named rather than hidden: a
// series' episodes cost one request per season, because TMDB has no endpoint
// returning a show's whole episode list; and a film's franchise costs one more,
// because the detail carries only the collection's name and id.
func (c *Client) Detail(ctx context.Context, nativeType, id string) (Title, error) {
	if nativeType != typeMovie && nativeType != typeTV {
		return Title{}, fmt.Errorf("unsupported TMDB type %q; expected %q or %q", nativeType, typeMovie, typeTV)
	}

	params := url.Values{}
	params.Set("append_to_response", appendFor(nativeType))
	// Without this, `images` returns only assets tagged with the request
	// language and a show whose logo is untagged appears to have none. The
	// explicit null is TMDB's spelling of "language-neutral", which is where most
	// clearlogos actually live.
	params.Set("include_image_language", imageLanguages(c.language))

	var raw rawTitle
	if err := c.get(ctx, "/"+nativeType+"/"+id, params, &raw); err != nil {
		return Title{}, err
	}

	title := c.title(raw, nativeType)

	if nativeType == typeTV {
		episodes, err := c.episodes(ctx, id, raw.Seasons)
		if err != nil {
			return Title{}, err
		}
		title.Episodes = episodes
	}

	// The franchise, when there is one. Best-effort: a detail screen without its
	// collection rail is a smaller thing than a detail screen that will not
	// render, and the name is already in hand either way.
	if raw.BelongsToCollection != nil {
		collection, err := c.collection(ctx, raw.BelongsToCollection.ID)
		if err == nil {
			title.Collection = collection
		} else {
			title.Collection = &Collection{Name: raw.BelongsToCollection.Name}
		}
	}

	return title, nil
}

// appendFor is the sub-request list for a type. It differs between films and
// series because TMDB spells the same concept differently for each: a film's age
// rating lives in `release_dates` (per country, per release), a series' in
// `content_ratings` (per country, flat).
func appendFor(nativeType string) string {
	// "watch/providers" carries a slash, which is how TMDB names that sub-request
	// and also how it keys the result — it is not a typo and must not be
	// "normalised" to an underscore.
	common := "credits,images,external_ids,keywords,recommendations,videos,watch/providers"
	if nativeType == typeTV {
		return common + ",content_ratings"
	}
	return common + ",release_dates"
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

// collection fetches a franchise and its members.
func (c *Client) collection(ctx context.Context, id int) (*Collection, error) {
	var raw struct {
		Name         string       `json:"name"`
		Overview     string       `json:"overview"`
		PosterPath   string       `json:"poster_path"`
		BackdropPath string       `json:"backdrop_path"`
		Parts        []rawPreview `json:"parts"`
	}
	if err := c.get(ctx, "/collection/"+strconv.Itoa(id), nil, &raw); err != nil {
		return nil, err
	}

	items := make([]Preview, 0, len(raw.Parts))
	for _, p := range raw.Parts {
		items = append(items, c.preview(p, typeMovie))
	}
	// Chronological, which is the order a franchise rail wants and not the order
	// TMDB returns — its `parts` come back in popularity order, so the third film
	// leads.
	sort.SliceStable(items, func(i, j int) bool { return items[i].Year < items[j].Year })

	return &Collection{
		Name:     raw.Name,
		Overview: raw.Overview,
		Poster:   c.imageURL(raw.PosterPath, c.images.poster),
		Backdrop: c.imageURL(raw.BackdropPath, c.images.backdrop),
		Items:    items,
	}, nil
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
				out = append(out, c.episode(e, number))
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

// Preview is one search, catalog, franchise or recommendation result — enough to
// render a row and to address the title later.
type Preview struct {
	ID         string
	NativeType string
	Title      string
	Year       int
	Poster     string
}

// Title is one film or series, fully described.
type Title struct {
	ID         string
	NativeType string
	IMDbID     string
	// TVDbID is present for series only — TVDB is television-focused and TMDB
	// reports no such id for a film. It is bound alongside the others so a title
	// added here dedups against a TVDB-keyed source.
	TVDbID     string
	WikidataID string
	Title      string
	Year       int
	Overview   string
	Poster     string
	Backdrop   string
	Logo       string
	Genres     []string
	Keywords   []string
	// Certification is the age rating for the configured region, empty when TMDB
	// has none for it.
	Certification string
	Rating        float64
	Runtime       string
	Cast          []Credit
	Trailers      []Trailer
	// Similar are TMDB's recommendations — the editorially better of its two
	// related-titles endpoints. `similar` is derived from shared genres and
	// keywords and returns markedly worse suggestions.
	Similar []Preview
	// Episodes is populated for a series only, in season/episode order.
	Episodes []Episode
	// Collection is the franchise a film belongs to, nil otherwise.
	Collection *Collection
	// Watch is where the title can be streamed, rented or bought in the
	// configured region — nil when no region is set or TMDB knows of nothing
	// there. It describes availability *elsewhere* and is never a source.
	Watch *WatchAvailability
}

// WatchAvailability is one region's streaming availability.
type WatchAvailability struct {
	Region      string
	Link        string
	Attribution string
	Offers      []WatchOffer
}

// WatchOffer is one service the title is on, and on what terms.
type WatchOffer struct {
	Provider string
	Logo     string
	Type     string
}

// Collection is a franchise and its members.
type Collection struct {
	Name     string
	Overview string
	Poster   string
	Backdrop string
	Items    []Preview
}

// Credit is one billed cast member.
type Credit struct {
	Name      string
	Character string
	Photo     string
}

// Trailer is one promotional video.
type Trailer struct {
	Name     string
	Site     string
	Key      string
	Official bool
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
func (c *Client) preview(r rawPreview, nativeType string) Preview {
	title, date := r.Title, r.ReleaseDate
	if nativeType == typeTV {
		title, date = r.Name, r.FirstAirDate
	}
	return Preview{
		ID:         strconv.Itoa(r.ID),
		NativeType: nativeType,
		Title:      title,
		Year:       parseYear(date),
		Poster:     c.imageURL(r.PosterPath, c.images.poster),
	}
}

type rawTitle struct {
	ID             int     `json:"id"`
	Title          string  `json:"title"`
	Name           string  `json:"name"`
	Overview       string  `json:"overview"`
	ReleaseDate    string  `json:"release_date"`
	FirstAirDate   string  `json:"first_air_date"`
	PosterPath     string  `json:"poster_path"`
	BackdropPath   string  `json:"backdrop_path"`
	VoteAverage    float64 `json:"vote_average"`
	Runtime        int     `json:"runtime"`
	EpisodeRunTime []int   `json:"episode_run_time"`
	Genres         []struct {
		Name string `json:"name"`
	} `json:"genres"`
	Seasons             []rawSeasonSummary `json:"seasons"`
	BelongsToCollection *struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"belongs_to_collection"`
	Credits struct {
		Cast []rawCast `json:"cast"`
	} `json:"credits"`
	Images struct {
		Logos []rawImage `json:"logos"`
	} `json:"images"`
	ExternalIDs struct {
		IMDbID     string `json:"imdb_id"`
		TVDbID     int    `json:"tvdb_id"`
		WikidataID string `json:"wikidata_id"`
	} `json:"external_ids"`
	Keywords struct {
		// TMDB spells the same list two ways: `keywords` on a film and `results`
		// on a series. Decoding one and not the other is a silent empty list.
		Movie  []rawKeyword `json:"keywords"`
		Series []rawKeyword `json:"results"`
	} `json:"keywords"`
	Recommendations struct {
		Results []rawPreview `json:"results"`
	} `json:"recommendations"`
	Videos struct {
		Results []rawVideo `json:"results"`
	} `json:"videos"`
	ReleaseDates struct {
		Results []rawReleaseDates `json:"results"`
	} `json:"release_dates"`
	ContentRatings struct {
		Results []rawContentRating `json:"results"`
	} `json:"content_ratings"`
	// Keyed by ISO 3166-1 country code. TMDB returns every region it knows, which
	// for a well-distributed film is over a hundred; only the configured one is
	// read.
	WatchProviders struct {
		Results map[string]rawWatchRegion `json:"results"`
	} `json:"watch/providers"`
}

// rawWatchRegion is one country's availability. TMDB splits the offers by how
// you get the title rather than tagging each one, so the offer type is the field
// name and has to be recovered from it.
type rawWatchRegion struct {
	Link     string             `json:"link"`
	Flatrate []rawWatchProvider `json:"flatrate"`
	Free     []rawWatchProvider `json:"free"`
	Ads      []rawWatchProvider `json:"ads"`
	Rent     []rawWatchProvider `json:"rent"`
	Buy      []rawWatchProvider `json:"buy"`
}

type rawWatchProvider struct {
	ProviderName    string `json:"provider_name"`
	LogoPath        string `json:"logo_path"`
	DisplayPriority int    `json:"display_priority"`
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

type rawKeyword struct {
	Name string `json:"name"`
}

type rawVideo struct {
	Name     string `json:"name"`
	Site     string `json:"site"`
	Key      string `json:"key"`
	Type     string `json:"type"`
	Official bool   `json:"official"`
}

type rawReleaseDates struct {
	CountryCode  string `json:"iso_3166_1"`
	ReleaseDates []struct {
		Certification string `json:"certification"`
	} `json:"release_dates"`
}

type rawContentRating struct {
	CountryCode string `json:"iso_3166_1"`
	Rating      string `json:"rating"`
}

type rawEpisode struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	StillPath     string `json:"still_path"`
	AirDate       string `json:"air_date"`
}

func (c *Client) episode(r rawEpisode, season int) Episode {
	return Episode{
		Season:    season,
		Episode:   r.EpisodeNumber,
		Title:     r.Name,
		Overview:  r.Overview,
		Thumbnail: c.imageURL(r.StillPath, c.images.still),
		Released:  r.AirDate,
	}
}

// maxCast is how many billed cast members a detail carries. A detail screen
// shows the *top* cast; TMDB returns the entire credited ensemble, which for a
// large production is hundreds of people and megabytes of headshots.
const maxCast = 18

// title translates a full TMDB record into the module's own shape.
func (c *Client) title(r rawTitle, nativeType string) Title {
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
	for _, m := range cast {
		credits = append(credits, Credit{
			Name:      m.Name,
			Character: m.Character,
			Photo:     c.imageURL(m.ProfilePath, c.images.profile),
		})
	}

	similar := make([]Preview, 0, maxSimilar)
	for _, p := range r.Recommendations.Results {
		if len(similar) >= maxSimilar {
			break
		}
		// A recommendation carries its own media_type, because TMDB will
		// recommend a series alongside a film.
		kind := p.MediaType
		if kind != typeMovie && kind != typeTV {
			kind = nativeType
		}
		similar = append(similar, c.preview(p, kind))
	}

	out := Title{
		ID:            strconv.Itoa(r.ID),
		NativeType:    nativeType,
		IMDbID:        strings.TrimSpace(r.ExternalIDs.IMDbID),
		WikidataID:    strings.TrimSpace(r.ExternalIDs.WikidataID),
		Title:         name,
		Year:          parseYear(date),
		Overview:      r.Overview,
		Poster:        c.imageURL(r.PosterPath, c.images.poster),
		Backdrop:      c.imageURL(r.BackdropPath, c.images.backdrop),
		Logo:          c.pickLogo(r.Images.Logos),
		Genres:        genres,
		Keywords:      keywordsOf(r),
		Certification: c.certificationOf(r, nativeType),
		Rating:        r.VoteAverage,
		Runtime:       runtimeLabel(r.Runtime, r.EpisodeRunTime),
		Cast:          credits,
		Trailers:      trailersOf(r.Videos.Results),
		Similar:       similar,
		Watch:         c.watchOf(r),
	}
	// TVDB reports television only, and TMDB renders "no id" as 0 rather than by
	// omitting the field.
	if r.ExternalIDs.TVDbID > 0 {
		out.TVDbID = strconv.Itoa(r.ExternalIDs.TVDbID)
	}
	return out
}

// keywordsOf reads the keyword list from whichever of TMDB's two spellings the
// response used.
func keywordsOf(r rawTitle) []string {
	raw := r.Keywords.Movie
	if len(raw) == 0 {
		raw = r.Keywords.Series
	}
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, k := range raw {
		if name := strings.TrimSpace(k.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// certificationOf reads the age rating for the configured region.
//
// It returns empty rather than falling back to another country's rating, which
// is the whole point: a US "R" shown to a household that set GB is not a
// conservative approximation, it is a different scale reported as if it were
// theirs. Empty means unknown, and a consumer must not read that as permissive.
func (c *Client) certificationOf(r rawTitle, nativeType string) string {
	region := c.region
	if region == "" {
		return ""
	}
	if nativeType == typeTV {
		for _, rating := range r.ContentRatings.Results {
			if strings.EqualFold(rating.CountryCode, region) {
				return strings.TrimSpace(rating.Rating)
			}
		}
		return ""
	}
	for _, country := range r.ReleaseDates.Results {
		if !strings.EqualFold(country.CountryCode, region) {
			continue
		}
		// A country has several dated releases (cinema, digital, physical) and
		// only some carry a certification.
		for _, release := range country.ReleaseDates {
			if cert := strings.TrimSpace(release.Certification); cert != "" {
				return cert
			}
		}
	}
	return ""
}

// justWatchAttribution is who compiles TMDB's availability data. TMDB's terms
// require crediting them wherever it is shown, which is why it travels in the
// value rather than being something a screen has to remember.
const justWatchAttribution = "JustWatch"

// watchOf reads the configured region's availability.
//
// It is region-exact for the same reason the certification is: availability
// differs entirely by country, and showing a viewer in Britain that something is
// on a service that carries it only in the United States is worse than showing
// nothing. No region configured means no claim — TMDB returns over a hundred
// regions for a well-distributed film and picking one would be inventing an
// answer.
func (c *Client) watchOf(r rawTitle) *WatchAvailability {
	if c.region == "" {
		return nil
	}
	region, ok := r.WatchProviders.Results[strings.ToUpper(c.region)]
	if !ok {
		return nil
	}

	// TMDB groups offers by how you get the title rather than tagging each one,
	// so the type is recovered from which list the entry came out of. The order
	// here is the order they are presented in: what a viewer may already pay for
	// first, what costs money now last.
	groups := []struct {
		offerType string
		providers []rawWatchProvider
	}{
		{"subscription", region.Flatrate},
		{"free", region.Free},
		{"ads", region.Ads},
		{"rent", region.Rent},
		{"buy", region.Buy},
	}

	var offers []WatchOffer
	seen := make(map[string]bool)
	for _, group := range groups {
		providers := append([]rawWatchProvider(nil), group.providers...)
		sort.SliceStable(providers, func(i, j int) bool {
			return providers[i].DisplayPriority < providers[j].DisplayPriority
		})
		for _, p := range providers {
			if p.ProviderName == "" {
				continue
			}
			// A service commonly appears under several types — rent *and* buy is
			// the norm. The first is the best terms on offer, since the groups are
			// ordered that way, so a later duplicate adds nothing a viewer needs.
			if seen[p.ProviderName] {
				continue
			}
			seen[p.ProviderName] = true
			offers = append(offers, WatchOffer{
				Provider: p.ProviderName,
				Logo:     c.imageURL(p.LogoPath, c.images.logo),
				Type:     group.offerType,
			})
		}
	}

	// A region entry with a link but no offers is real — TMDB has a page for the
	// title, nothing carries it there. That is worth returning: it is "none known
	// here", which is not the same as having no answer at all.
	if len(offers) == 0 && region.Link == "" {
		return nil
	}
	return &WatchAvailability{
		Region:      strings.ToUpper(c.region),
		Link:        region.Link,
		Attribution: justWatchAttribution,
		Offers:      offers,
	}
}

// trailersOf keeps the promotional videos, dropping the featurettes, clips and
// behind-the-scenes reels TMDB returns in the same list. Official entries lead.
func trailersOf(videos []rawVideo) []Trailer {
	var out []Trailer
	for _, v := range videos {
		if v.Key == "" {
			continue
		}
		if !strings.EqualFold(v.Type, "Trailer") && !strings.EqualFold(v.Type, "Teaser") {
			continue
		}
		out = append(out, Trailer{Name: v.Name, Site: v.Site, Key: v.Key, Official: v.Official})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Official && !out[j].Official })
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
func (c *Client) pickLogo(logos []rawImage) string {
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
	return c.imageURL(logos[best].FilePath, c.images.logo)
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
func (c *Client) imageURL(path, size string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	return c.images.base + size + path
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
