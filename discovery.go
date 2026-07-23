package tmdb

import (
	"context"
	"fmt"

	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

// The two discovery roles. They are what produce a ref in the first place: with
// only RoleMetadata a deployment could describe content it had no way to name,
// which is why ADR 0035 makes metadata *and* search one required capability
// class rather than two.

// Search returns virtual candidates for free text (RoleSearch). It is TMDB's
// multi-search, so one query covers film and television and the media-type
// filter narrows the answer rather than choosing the endpoint.
func (c *Capability) Search(ctx context.Context, req v1.SearchRequest) (v1.SearchResponse, error) {
	client, err := c.clientFrom(ctx, req.Settings)
	if err != nil {
		return v1.SearchResponse{}, err
	}

	previews, err := client.Search(ctx, req.Text)
	if err != nil {
		return v1.SearchResponse{}, fmt.Errorf("search TMDB: %w", err)
	}

	results := make([]v1.SearchResult, 0, len(previews))
	for _, p := range previews {
		if req.MediaType != "" && mediaTypeFor(p.NativeType) != req.MediaType {
			continue
		}
		if req.Limit > 0 && len(results) >= req.Limit {
			break
		}
		results = append(results, v1.SearchResult{
			Ref: refFrom(p), Title: p.Title, Year: p.Year, Poster: p.Poster,
		})
	}
	return v1.SearchResponse{Results: results}, nil
}

// Catalogs lists the collections this module exposes (RoleCatalog) — a curated
// built-in set plus whatever `/discover` queries the user has defined.
//
// TMDB has endpoints rather than a catalog manifest, so unlike a Stremio addon
// there is nobody to ask what collections exist and the built-in set is somebody's
// choice. `/discover` is what stops that choice being the ceiling. Nothing in the
// SDK had to grow for it: this role already returned a list rather than a
// constant, so user-defined catalogs were a settings question all along.
func (c *Capability) Catalogs(ctx context.Context, req v1.CatalogsRequest) (v1.CatalogsResponse, error) {
	client, err := c.clientFrom(ctx, req.Settings)
	if err != nil {
		return v1.CatalogsResponse{}, err
	}

	decls := client.Catalogs()
	catalogs := make([]v1.Catalog, 0, len(decls))
	for _, d := range decls {
		catalogs = append(catalogs, v1.Catalog{ID: d.ID, NativeType: d.Type, Name: d.Name})
	}
	return v1.CatalogsResponse{Catalogs: catalogs}, nil
}

// CatalogItems lists one collection's entries as virtual candidates (ADR 0028).
// It touches no part of the object graph — browsing a source must not flood the
// library with everything the source knows about.
func (c *Capability) CatalogItems(ctx context.Context, req v1.CatalogItemsRequest) (v1.CatalogItemsResponse, error) {
	client, err := c.clientFrom(ctx, req.Settings)
	if err != nil {
		return v1.CatalogItemsResponse{}, err
	}

	previews, err := client.CatalogItems(ctx, req.CatalogID, req.NativeType, req.Skip)
	if err != nil {
		return v1.CatalogItemsResponse{}, fmt.Errorf("list TMDB catalog items: %w", err)
	}

	items := make([]v1.CatalogItem, 0, len(previews))
	for _, p := range previews {
		items = append(items, v1.CatalogItem{
			Ref: refFrom(p), Title: p.Title, Year: p.Year, Poster: p.Poster,
		})
	}
	return v1.CatalogItemsResponse{Items: items}, nil
}
