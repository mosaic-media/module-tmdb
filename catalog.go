package tmdb

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Catalogs, and the discover queries behind the user-defined ones.
//
// TMDB has no catalog manifest — it has endpoints — so unlike a Stremio addon
// there is nobody to ask what collections exist. The built-in set below is
// therefore a curated choice, and `/discover` is what stops that choice being
// the ceiling: a user can define their own ("French thrillers, rated above
// seven") and it becomes a browsable catalog like any other. Nothing in the SDK
// had to change for that — `CatalogProvider.Catalogs` already returns a list
// rather than a constant.

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
		})
	}
	return out
}

// reservedDiscoverParams are the parameters this module sets itself. A
// user-supplied query must not be able to set them.
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
func (c *Client) CatalogItems(ctx context.Context, catalogID, nativeType string, skip int) ([]Preview, error) {
	decl, ok := c.findCatalog(catalogID, nativeType)
	if !ok {
		return nil, fmt.Errorf("unknown catalog %q for type %q", catalogID, nativeType)
	}

	params := url.Values{}
	if decl.query != "" {
		// Already sanitised when the declaration was built, so a reserved
		// parameter cannot reach here and the module's own values below win by
		// being set afterwards.
		parsed, err := url.ParseQuery(decl.query)
		if err != nil {
			return nil, fmt.Errorf("catalog %q has an unusable query: %w", decl.Name, err)
		}
		params = parsed
	}
	params.Set("page", strconv.Itoa(skip/catalogPage+1))
	params.Set("include_adult", strconv.FormatBool(c.adult))
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
		// A list or discover endpoint's results carry no media_type — the endpoint
		// *is* the type — so it comes from the catalog rather than from the record.
		// The trending endpoints do return one; taking the declaration's either way
		// keeps a single path.
		out = append(out, c.preview(r, decl.Type))
	}
	return out, nil
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
