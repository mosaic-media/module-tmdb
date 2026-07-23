package tmdb

import (
	"context"
	"sync"
	"time"
)

// The image CDN configuration.
//
// A TMDB record carries an image *path* ("/xyz.jpg"), never a URL: the base host
// and the size are the caller's choice, so the URL does not exist until this
// module builds it. TMDB publishes both through `/configuration` and documents
// that a client should read them rather than hardcode them.
//
// **The fallback below is the same set that was hardcoded, and it stays.** The
// values have not changed in a decade, and a metadata module that cannot render
// a poster because a configuration call failed would have turned a robustness
// improvement into an outage. So the fetch is best-effort, cached for a day, and
// never on the critical path of a first request.

// imageConfig is the resolved CDN base and the size to request per surface.
type imageConfig struct {
	base     string
	poster   string
	backdrop string
	logo     string
	profile  string
	still    string
}

// defaultImageConfig is what the module uses until (and if) `/configuration`
// says otherwise. The sizes are the ones each surface actually renders at, so a
// card does not download a 2000px poster to draw it 200px wide.
var defaultImageConfig = imageConfig{
	base:     "https://image.tmdb.org/t/p/",
	poster:   "w500",
	backdrop: "w1280",
	logo:     "w500",
	profile:  "w185",
	still:    "w300",
}

// imageConfigTTL is how long a fetched configuration is trusted. A day is
// generous to the point of arbitrary, which is the right shape for a value that
// has not changed since the API launched.
const imageConfigTTL = 24 * time.Hour

// imageConfigCache holds the last successful fetch. It lives on the Capability
// rather than the Client because a Client is built per invocation from the
// settings the Platform hands in, and re-fetching the CDN layout on every
// metadata call would be a request per detail screen to learn something that
// never changes.
type imageConfigCache struct {
	mu      sync.Mutex
	value   imageConfig
	fetched time.Time
}

// get returns the cached configuration, refreshing it when stale.
//
// Every failure path returns the previous value — the default on a cold cache —
// so a caller never has to handle "no configuration". The lock is held across
// the fetch, which serialises concurrent refreshes into one request rather than
// letting a burst of detail screens each issue their own.
func (c *imageConfigCache) get(ctx context.Context, fetch func(context.Context) (imageConfig, error)) imageConfig {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.value.base == "" {
		c.value = defaultImageConfig
	}
	if time.Since(c.fetched) < imageConfigTTL {
		return c.value
	}

	// Stamped before the result is known, so a source that is failing is retried
	// on the next TTL rather than on every single call.
	c.fetched = time.Now()

	fetched, err := fetch(ctx)
	if err != nil {
		return c.value
	}
	c.value = fetched
	return c.value
}

// fetchImageConfig reads `/configuration` and resolves the size to use per
// surface. It is a method on Client because it needs the credential, which is
// the one thing about it that is per-invocation.
func (c *Client) fetchImageConfig(ctx context.Context) (imageConfig, error) {
	var raw struct {
		Images struct {
			SecureBaseURL string   `json:"secure_base_url"`
			PosterSizes   []string `json:"poster_sizes"`
			BackdropSizes []string `json:"backdrop_sizes"`
			LogoSizes     []string `json:"logo_sizes"`
			ProfileSizes  []string `json:"profile_sizes"`
			StillSizes    []string `json:"still_sizes"`
		} `json:"images"`
	}
	if err := c.get(ctx, "/configuration", nil, &raw); err != nil {
		return imageConfig{}, err
	}

	out := defaultImageConfig
	if raw.Images.SecureBaseURL != "" {
		out.base = raw.Images.SecureBaseURL
	}
	out.poster = pickSize(raw.Images.PosterSizes, defaultImageConfig.poster)
	out.backdrop = pickSize(raw.Images.BackdropSizes, defaultImageConfig.backdrop)
	out.logo = pickSize(raw.Images.LogoSizes, defaultImageConfig.logo)
	out.profile = pickSize(raw.Images.ProfileSizes, defaultImageConfig.profile)
	out.still = pickSize(raw.Images.StillSizes, defaultImageConfig.still)
	return out, nil
}

// pickSize keeps the preferred size when the server still offers it, and
// otherwise falls back to the largest non-"original" size available.
//
// "original" is deliberately not the fallback: it is unbounded, and a poster
// rail that silently started serving 4000px source scans would be a performance
// regression nothing reported.
func pickSize(available []string, preferred string) string {
	if len(available) == 0 {
		return preferred
	}
	best := ""
	for _, size := range available {
		if size == preferred {
			return preferred
		}
		if size != "original" {
			best = size
		}
	}
	if best == "" {
		return preferred
	}
	return best
}
