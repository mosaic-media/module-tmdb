package tmdb

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"time"

	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

// The narrowings a catalog accepts, and where their values come from.
//
// A catalog filter is *declared with its permitted values* (SDK v0.25.0), so
// the consumer builds a control from this source's own list and can never send
// a value TMDB did not name. That is the whole reason these are fetched rather
// than written down here: TMDB's genre list is versioned data behind an
// endpoint, and a hardcoded copy is wrong the first time a genre is renamed
// with nothing to report it.
//
// **The filter names are TMDB's own discover parameters.** `CatalogFilter.Name`
// is documented as source-native and opaque to the Platform, so using
// `with_genres` rather than a Mosaic-flavoured "genre" means the selection needs
// no translation on the way back in — it is already the query parameter. That
// keeps this module's dialect inside this module, which is the point of an
// anti-corruption layer: the Platform carries a string it never interprets.

// filterGenre and filterWatchProvider are the two narrowings this module
// offers. They are TMDB discover parameter names.
const (
	filterGenre         = "with_genres"
	filterWatchProvider = "with_watch_providers"
)

// facetTTL is how long a fetched option list is trusted. Genres change on the
// order of once a decade and a streaming service's presence in a region changes
// a few times a year; an hour would be a request per catalog render to learn
// something that has not moved.
const facetTTL = 24 * time.Hour

// facets are the filter declarations for one native type.
type facets struct {
	genres         []v1.CatalogFilterOption
	watchProviders []v1.CatalogFilterOption
}

// filters renders the declarations a catalog carries, dropping any whose option
// list is empty.
//
// **An empty list is dropped rather than declared**, because a filter with no
// options cannot be exercised: a consumer would draw an empty control, and a
// control that does nothing is the failure mode this whole mechanism is shaped
// to avoid. A failed or unconfigured fetch therefore reads as "this catalog
// offers no narrowing", which is exactly what it is.
func (f facets) filters() []v1.CatalogFilter {
	var out []v1.CatalogFilter
	if len(f.genres) > 0 {
		out = append(out, v1.CatalogFilter{Name: filterGenre, Label: "Genre", Options: f.genres})
	}
	if len(f.watchProviders) > 0 {
		out = append(out, v1.CatalogFilter{
			Name: filterWatchProvider, Label: "Streaming service", Options: f.watchProviders,
		})
	}
	return out
}

// facetCache holds the fetched option lists per native type. It lives on the
// Capability rather than the Client for the same reason imageConfigCache does:
// a Client is built per invocation from the settings the Platform hands in, and
// re-fetching a genre list on every catalog render would be a request per screen
// to learn something that never changes.
//
// It is keyed by native type *and region*, because the watch-provider list is
// regional — the services carrying titles in Ireland are not the services
// carrying them in Japan, and a cache that ignored the region would serve one
// deployment's list to another after a settings change.
type facetCache struct {
	mu      sync.Mutex
	entries map[string]facetEntry
}

type facetEntry struct {
	value   facets
	fetched time.Time
}

// get returns the cached declarations for one native type and region,
// refreshing them when stale.
//
// Every failure path returns the previous value — an empty facets on a cold
// cache — so a caller never has to handle "could not fetch". A catalog then
// declares no filters and renders exactly as it did before this existed, which
// is the correct degradation: an absent control is visibly absent.
func (c *facetCache) get(ctx context.Context, nativeType, region string, fetch func(context.Context, string, string) (facets, error)) facets {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = map[string]facetEntry{}
	}
	key := nativeType + "|" + region
	entry := c.entries[key]
	if time.Since(entry.fetched) < facetTTL {
		return entry.value
	}

	// Stamped before the result is known, so a source that is failing is retried
	// on the next TTL rather than on every single call.
	entry.fetched = time.Now()
	c.entries[key] = entry

	fetched, err := fetch(ctx, nativeType, region)
	if err != nil {
		return entry.value
	}
	entry.value = fetched
	c.entries[key] = entry
	return entry.value
}

// fetchFacets reads the option lists for one native type. It is a method on
// Client because it needs the credential, which is the one per-invocation thing
// about it.
//
// The two fetches are independent and a failure of either is not a failure of
// the other: a deployment with no region configured has no watch-provider list
// and still has genres.
func (c *Client) fetchFacets(ctx context.Context, nativeType, region string) (facets, error) {
	var out facets

	var genres struct {
		Genres []struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"genres"`
	}
	if err := c.get(ctx, "/genre/"+nativeType+"/list", nil, &genres); err != nil {
		return facets{}, err
	}
	for _, g := range genres.Genres {
		if g.ID == 0 || g.Name == "" {
			continue
		}
		out.genres = append(out.genres, v1.CatalogFilterOption{
			Value: strconv.Itoa(g.ID), Label: g.Name,
		})
	}

	// **No region, no watch-provider filter.** Availability is national
	// (see CLAUDE.md: a substitute region is a wrong answer rather than a partial
	// one), and `with_watch_providers` without `watch_region` is a query TMDB
	// answers with something nobody asked for. So the filter is simply not
	// offered on a deployment that has not said where it is.
	if region == "" {
		return out, nil
	}
	var providers struct {
		Results []struct {
			ID       int    `json:"provider_id"`
			Name     string `json:"provider_name"`
			Priority int    `json:"display_priority"`
		} `json:"results"`
	}
	params := url.Values{}
	params.Set("watch_region", region)
	if err := c.get(ctx, "/watch/providers/"+nativeType, params, &providers); err != nil {
		// Genres already succeeded, and returning them is better than discarding
		// both over the optional half.
		return out, nil
	}
	// TMDB's display_priority is its own ordering for the region, which is a
	// better answer than alphabetical: the services a user is likely to hold
	// come first. Ties break on name so the list is stable between renders.
	sort.SliceStable(providers.Results, func(i, j int) bool {
		if providers.Results[i].Priority != providers.Results[j].Priority {
			return providers.Results[i].Priority < providers.Results[j].Priority
		}
		return providers.Results[i].Name < providers.Results[j].Name
	})
	for _, p := range providers.Results {
		if p.ID == 0 || p.Name == "" {
			continue
		}
		out.watchProviders = append(out.watchProviders, v1.CatalogFilterOption{
			Value: strconv.Itoa(p.ID), Label: p.Name,
		})
	}
	return out, nil
}
