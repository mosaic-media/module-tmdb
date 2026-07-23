package tmdb

import (
	"context"
	"fmt"

	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

// Metadata resolves full descriptive detail for a ref (RoleMetadata). It backs
// the detail screen for both a virtual result and an in-library node, and it is
// the detail Import draws on (ADR 0027, ADR 0034).
//
// This is the role the module exists for. Everything it returns that a Stremio
// meta addon also returns is a convenience; the two fields that are not — the
// clearlogo and the cast headshots and character names — are the recorded gaps
// (ADR 0034) that no addon protocol carries, and they are why a metadata module
// was worth building rather than configuring another addon.
func (c *Capability) Metadata(ctx context.Context, req v1.MetadataRequest) (v1.ContentMetadata, error) {
	client, err := c.clientFrom(req.Settings)
	if err != nil {
		return v1.ContentMetadata{}, err
	}

	title, err := client.Detail(ctx, req.Ref.NativeType, req.Ref.NativeID)
	if err != nil {
		return v1.ContentMetadata{}, fmt.Errorf("fetch TMDB detail: %w", err)
	}

	// Through the SDK's ambient telemetry rather than a print (ADR 0059): this
	// lands in the Platform's records, attributed to this module and correlated
	// with the request that caused it. What is worth recording is which of the
	// expensive-to-obtain fields actually arrived — a detail screen missing its
	// logo is otherwise indistinguishable from a detail screen whose logo failed
	// to load.
	v1.TelemetryFrom(ctx).Debug("tmdb metadata resolved",
		v1.String("native_type", req.Ref.NativeType),
		v1.String("native_id", req.Ref.NativeID),
		v1.Bool("has_logo", title.Logo != ""),
		v1.Int("cast", len(title.Cast)),
		v1.Int("episodes", len(title.Episodes)))

	return v1.ContentMetadata{
		Ref:      req.Ref,
		Title:    title.Title,
		Year:     title.Year,
		Overview: title.Overview,
		Poster:   title.Poster,
		Backdrop: title.Backdrop,
		Logo:     title.Logo,
		Genres:   title.Genres,
		Rating:   title.Rating,
		Runtime:  title.Runtime,
		Cast:     castOf(title.Cast),
		Episodes: episodesOf(title.Episodes),
	}, nil
}

// castOf maps billed credits onto the SDK's Person. Unlike an addon that carries
// names only, every field here is populated: the character is what turns a list
// of actors into a cast list, and the photo is what turns it into a rail.
func castOf(credits []Credit) []v1.Person {
	if len(credits) == 0 {
		return nil
	}
	out := make([]v1.Person, 0, len(credits))
	for _, c := range credits {
		out = append(out, v1.Person{Name: c.Name, Role: c.Character, Photo: c.Photo})
	}
	return out
}

// episodesOf maps the episode list onto the SDK's read-only preview projection.
// It is deliberately not the materialised tree — Import builds that — but it is
// what lets a user read a series' episode list before deciding to add it
// (ADR 0034).
func episodesOf(episodes []Episode) []v1.EpisodePreview {
	if len(episodes) == 0 {
		return nil
	}
	out := make([]v1.EpisodePreview, 0, len(episodes))
	for _, e := range episodes {
		out = append(out, v1.EpisodePreview{
			Season:    e.Season,
			Episode:   e.Episode,
			Title:     episodeTitle(e),
			Overview:  e.Overview,
			Thumbnail: e.Thumbnail,
			Released:  e.Released,
		})
	}
	return out
}
