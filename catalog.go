package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

// Catalogs, and the discover queries behind the user-defined ones.
//
// TMDB has no catalog manifest — it has endpoints — so unlike a Stremio addon
// there is nobody to ask what collections exist. The built-in set below is
// therefore a curated choice, and `/discover` is what stops that choice being
// the ceiling: a user can define their own ("French thrillers, rated above
// seven") and it becomes a browsable catalog like any other.

// catalogPage is how many items one TMDB list page holds. It is fixed by the
// API, and it is what converts the Platform's item-offset Skip into a page
// number.
const catalogPage = 20

// customCatalogPrefix namespaces a user-defined catalog's id so it can never
// collide with a built-in one, and so the two are distinguishable when a
// settings screen offers to remove one.
const customCatalogPrefix = "custom:"

// CatalogDecl is one collection this module exposes. The path and query are
// unexported: which endpoint backs a catalog is this file's business, not a
// caller's.
type CatalogDecl struct {
	ID   string
	Type string
	Name string
	// path is the list endpoint, or the discover endpoint for a custom catalog.
	path string
	// query is the user's own discover parameters, empty for a built-in.
	query string
	// filterable is whether this catalog can be narrowed. It is a property of the
	// endpoint rather than a policy choice.
	//
	// TMDB's list endpoints — `/movie/popular`, `/trending/movie/week` — take no
	// `with_genres`. Only `/discover` filters, so a narrowed catalog is served by
	// a discover query that reproduces the same ranking, which is what
	// discoverQuery below holds. A catalog whose ranking has no faithful discover
	// equivalent declares no filters rather than accepting one and answering with
	// something adjacent: TMDB's trending is a computed popularity-over-time
	// ranking that `/discover` cannot express, and a "Trending · Action" row
	// silently served by raw popularity would be confidently wrong in the way a
	// user cannot see.
	filterable bool
	// discoverQuery is the base query of that equivalent, merged under the
	// caller's selected filters.
	discoverQuery string
}

// Custom is whether this catalog came from a user's settings rather than the
// built-in set — which is what a settings screen needs in order to offer a
// remove control only for the ones a user can actually remove.
func (d CatalogDecl) Custom() bool { return strings.HasPrefix(d.ID, customCatalogPrefix) }

// builtinCatalogs are the views a home screen renders out of the box. It is
// deliberately short: this is a floor a user extends, not an attempt to expose
// everything TMDB can answer.
func builtinCatalogs() []CatalogDecl {
	return []CatalogDecl{
		// Trending is TMDB's own popularity-over-time ranking and `/discover`
		// has no equivalent for it, so it takes no filter — see CatalogDecl.
		{ID: "trending", Type: typeMovie, Name: "Trending Films", path: "/trending/movie/week"},
		{ID: "trending", Type: typeTV, Name: "Trending Series", path: "/trending/tv/week"},
		// Popular and Top Rated are exactly a discover sort, which is why these
		// four are the ones that filter.
		{ID: "popular", Type: typeMovie, Name: "Popular Films", path: "/movie/popular",
			filterable: true, discoverQuery: "sort_by=popularity.desc"},
		{ID: "popular", Type: typeTV, Name: "Popular Series", path: "/tv/popular",
			filterable: true, discoverQuery: "sort_by=popularity.desc"},
		// The vote-count floor is TMDB's own published equivalent for its top-rated
		// lists, and it is load bearing: without it the first page is titles with
		// one ten-out-of-ten vote, which is a worse list wearing the same name.
		{ID: "top_rated", Type: typeMovie, Name: "Top Rated Films", path: "/movie/top_rated",
			filterable: true, discoverQuery: "sort_by=vote_average.desc&vote_count.gte=300"},
		{ID: "top_rated", Type: typeTV, Name: "Top Rated Series", path: "/tv/top_rated",
			filterable: true, discoverQuery: "sort_by=vote_average.desc&vote_count.gte=200"},
		// In Cinemas and On The Air are date windows TMDB computes and does not
		// publish the bounds of. A discover query with a window this module chose
		// would be a different list under the same name, so neither filters.
		{ID: "now_playing", Type: typeMovie, Name: "In Cinemas", path: "/movie/now_playing"},
		{ID: "on_the_air", Type: typeTV, Name: "On The Air", path: "/tv/on_the_air"},
	}
}

// catalogsFor composes the built-in catalogs with a user's own discover
// queries. A custom catalog whose name or query is empty is dropped rather than
// rendered as an unusable row.
func catalogsFor(custom []customCatalog) []CatalogDecl {
	out := builtinCatalogs()
	for i, c := range custom {
		name := strings.TrimSpace(c.Name)
		query := sanitiseDiscoverQuery(c.Query)
		if name == "" || query == "" {
			continue
		}
		nativeType := typeMovie
		if c.Type == typeTV {
			nativeType = typeTV
		}
		out = append(out, CatalogDecl{
			// Indexed rather than named: two catalogs may share a name, and the id
			// has to stay stable for the Platform to address the same one twice.
			ID:    customCatalogPrefix + strconv.Itoa(i),
			Type:  nativeType,
			Name:  name,
			path:  "/discover/" + nativeType,
			query: query,
			// A custom catalog is already a discover query, so it filters with no
			// equivalent to find. The user's own parameters are the base, and a
			// selected filter narrows within them rather than replacing them — a
			// user who wrote `with_original_language=fr` and then picks Thriller
			// gets French thrillers, which is the only reading that does not
			// discard something they typed.
			filterable:    true,
			discoverQuery: query,
		})
	}
	return out
}

// reservedDiscoverParams are the parameters this module sets itself. A
// user-supplied query must not be able to set them. Add to this map; never
// remove from it.
//
// `api_key` is the one that matters and the reason this function exists: a
// discover query is free text a user types into a settings screen, and without
// this a query reading `api_key=…` would replace the credential the module sends
// — silently, since the substitution happens in url.Values before the request is
// built. The rest are excluded because the module owns paging, language and
// safety, and a query that fought it would produce results nobody could explain.
var reservedDiscoverParams = map[string]bool{
	"api_key":       true,
	"language":      true,
	"page":          true,
	"include_adult": true,
}

// sanitiseDiscoverQuery parses a user's discover parameters and drops anything
// reserved. It returns the re-encoded remainder, empty when nothing survives.
func sanitiseDiscoverQuery(query string) string {
	parsed, err := url.ParseQuery(strings.TrimPrefix(strings.TrimSpace(query), "?"))
	if err != nil {
		return ""
	}
	for name := range parsed {
		if reservedDiscoverParams[strings.ToLower(name)] {
			parsed.Del(name)
		}
	}
	return parsed.Encode()
}

// Catalogs returns the catalogs this client exposes — built-in plus the user's
// own.
func (c *Client) Catalogs() []CatalogDecl { return c.catalogs }

// CatalogItems lists one catalog's entries. Skip is an item offset and TMDB
// pages in twenties, so it is converted rather than passed through; an offset
// that lands mid-page rounds down, which repeats at most nineteen items rather
// than skipping any.
//
// filters are already-validated discover parameters (see Capability.CatalogItems
// — this method trusts them because the caller checked them against the same
// declaration the consumer built its control from). A non-empty set switches the
// request from the catalog's list endpoint to its discover equivalent, because
// TMDB's list endpoints do not filter.
//
// It reports whether another page exists, which TMDB answers exactly:
// `total_pages` is in every list and discover response, so this is the strong
// form of the SDK's HasMore rather than an inference from a full page.
func (c *Client) CatalogItems(ctx context.Context, catalogID, nativeType string, skip int, filters map[string]string) ([]Preview, bool, error) {
	decl, ok := c.findCatalog(catalogID, nativeType)
	if !ok {
		return nil, false, fmt.Errorf("unknown catalog %q for type %q", catalogID, nativeType)
	}

	path := decl.path
	base := decl.query
	if len(filters) > 0 {
		if !decl.filterable {
			return nil, false, fmt.Errorf("catalog %q cannot be narrowed", decl.Name)
		}
		path = "/discover/" + decl.Type
		base = decl.discoverQuery
	}

	params := url.Values{}
	if base != "" {
		// Already sanitised when the declaration was built, so a reserved
		// parameter cannot reach here and the module's own values below win by
		// being set afterwards.
		parsed, err := url.ParseQuery(base)
		if err != nil {
			return nil, false, fmt.Errorf("catalog %q has an unusable query: %w", decl.Name, err)
		}
		params = parsed
	}
	for name, value := range filters {
		params.Set(name, value)
	}
	// `with_watch_providers` is meaningless without a region, and this module
	// declines to offer that filter at all when none is set — so reaching here
	// with one means a region exists. Setting it explicitly rather than relying
	// on the block below keeps the two independent: `region` scopes release
	// dates, `watch_region` scopes availability, and TMDB reads them separately.
	if _, ok := filters[filterWatchProvider]; ok && c.region != "" {
		params.Set("watch_region", c.region)
	}
	params.Set("page", strconv.Itoa(skip/catalogPage+1))
	params.Set("include_adult", strconv.FormatBool(c.adult))
	if c.region != "" {
		params.Set("region", c.region)
	}

	var resp struct {
		Page       int          `json:"page"`
		TotalPages int          `json:"total_pages"`
		Results    []rawPreview `json:"results"`
	}
	if err := c.get(ctx, path, params, &resp); err != nil {
		return nil, false, err
	}

	out := make([]Preview, 0, len(resp.Results))
	for _, r := range resp.Results {
		// A list or discover endpoint's results carry no media_type — the endpoint
		// is the type — so it comes from the catalog rather than from the record.
		// The trending endpoints do return one; taking the declaration's either way
		// keeps a single path.
		out = append(out, c.preview(r, decl.Type))
	}
	return out, resp.Page > 0 && resp.Page < resp.TotalPages, nil
}

// narrowingFor validates a caller's selected filters against what a catalog
// actually declared, and returns them as discover parameters.
//
// An unrecognised name or value is an error, not something to drop. The SDK is
// explicit that a provider must decline a filter it does not understand rather
// than answering with the unfiltered page, because a silently widened query
// returns a plausible list for a question nobody asked — and unlike an absent
// control, a user cannot see it. The value is checked against the declared
// options for the same reason: `with_genres` accepts arbitrary text and TMDB
// answers an unknown genre id with an unfiltered page, so passing it on would
// produce exactly the failure this refuses.
func narrowingFor(decl CatalogDecl, declared []v1.CatalogFilter, selected map[string]string) (map[string]string, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	if !decl.filterable {
		return nil, fmt.Errorf("catalog %q cannot be narrowed", decl.Name)
	}
	out := make(map[string]string, len(selected))
	for name, value := range selected {
		filter, ok := findFilter(declared, name)
		if !ok {
			return nil, fmt.Errorf("catalog %q has no filter named %q", decl.Name, name)
		}
		if !hasOption(filter.Options, value) {
			return nil, fmt.Errorf("filter %q on catalog %q has no option %q", name, decl.Name, value)
		}
		out[name] = value
	}
	return out, nil
}

func findFilter(filters []v1.CatalogFilter, name string) (v1.CatalogFilter, bool) {
	for _, f := range filters {
		if f.Name == name {
			return f, true
		}
	}
	return v1.CatalogFilter{}, false
}

func hasOption(options []v1.CatalogFilterOption, value string) bool {
	for _, o := range options {
		if o.Value == value {
			return true
		}
	}
	return false
}

// findCatalog resolves a catalog declaration by its id and type. Two catalogs
// share an id ("popular" for film and for television), so the type is part of
// the key rather than decoration.
func (c *Client) findCatalog(id, nativeType string) (CatalogDecl, bool) {
	for _, decl := range c.catalogs {
		if decl.ID == id && decl.Type == nativeType {
			return decl, true
		}
	}
	return CatalogDecl{}, false
}
