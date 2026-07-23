package tmdb_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	v1 "github.com/mosaic-media/sdk/contracts/platform/v1"
)

// The hermetic test rig. These tests run the capability against a fake TMDB over
// httptest and an in-memory ContentService, so they prove this module's own
// behaviour: the translation of TMDB's wire shapes onto the SDK's typed fields,
// and of a title onto the Platform's object graph. The end-to-end path through
// the Platform's registry and real PostgreSQL is a separate test in the platform
// repository.
//
// **The fake is reached by rewriting the request host, not by making the API
// base configurable.** The base URL is a constant on purpose — there is exactly
// one TMDB — and adding a settable field so tests can point elsewhere would put
// a seam in the production type that only tests use. Since the module already
// accepts the Platform's http.Client, a transport that redirects
// api.themoviedb.org is a complete injection through a seam that had to exist
// anyway.

// redirect returns an http.Client whose every request is sent to server instead
// of to its real host, preserving path and query.
func redirect(server *httptest.Server) *http.Client {
	base, _ := url.Parse(server.URL)
	return &http.Client{Transport: rewriteHost{base: base, inner: server.Client().Transport}}
}

type rewriteHost struct {
	base  *url.URL
	inner http.RoundTripper
}

func (r rewriteHost) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = r.base.Scheme
	req.URL.Host = r.base.Host
	req.Host = r.base.Host
	inner := r.inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(req)
}

// fakeTMDB serves the subset of the v3 API this module uses, with one film and
// one two-season series. Requests it does not recognise are a 404 with TMDB's
// own error shape, so an unexpected call fails as a test failure rather than as
// a silently empty result.
func fakeTMDB() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/3/search/multi", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{
			// A person result, which must be dropped: nothing downstream could
			// materialise one.
			map[string]any{"id": 500, "media_type": "person", "name": "Denis Villeneuve"},
			map[string]any{
				"id": 335984, "media_type": "movie", "title": "Blade Runner 2049",
				"release_date": "2017-10-04", "poster_path": "/poster.jpg",
			},
			map[string]any{
				"id": 1396, "media_type": "tv", "name": "Breaking Bad",
				"first_air_date": "2008-01-20", "poster_path": "/bb.jpg",
			},
		}})
	})

	mux.HandleFunc("/3/movie/335984", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id": 335984, "title": "Blade Runner 2049",
			"overview":      "Thirty years after the events of the first film.",
			"release_date":  "2017-10-04",
			"poster_path":   "/poster.jpg",
			"backdrop_path": "/backdrop.jpg",
			"vote_average":  7.6,
			"runtime":       164,
			"genres": []any{
				map[string]any{"name": "Science Fiction"},
				map[string]any{"name": "Drama"},
			},
			"belongs_to_collection": map[string]any{"id": 344, "name": "Blade Runner Collection"},
			"credits": map[string]any{"cast": []any{
				// Deliberately out of billing order, to prove the module sorts.
				map[string]any{"name": "Ana de Armas", "character": "Joi", "profile_path": "/ana.jpg", "order": 2},
				map[string]any{"name": "Ryan Gosling", "character": "K", "profile_path": "/ryan.jpg", "order": 0},
				map[string]any{"name": "Harrison Ford", "character": "Rick Deckard", "profile_path": "/harrison.jpg", "order": 1},
			}},
			"images": map[string]any{"logos": []any{
				map[string]any{"file_path": "/neutral.png", "iso_639_1": nil, "vote_average": 9},
				map[string]any{"file_path": "/english.png", "iso_639_1": "en", "vote_average": 5},
			}},
			"external_ids": map[string]any{"imdb_id": "tt1856101", "wikidata_id": "Q18704460"},
			// A film spells its keyword list `keywords`; a series spells the same
			// list `results`.
			"keywords": map[string]any{"keywords": []any{
				map[string]any{"name": "dystopia"},
				map[string]any{"name": "replicant"},
			}},
			"recommendations": map[string]any{"results": []any{
				map[string]any{"id": 78, "media_type": "movie", "title": "Blade Runner", "release_date": "1982-06-25", "poster_path": "/br.jpg"},
			}},
			"videos": map[string]any{"results": []any{
				map[string]any{"name": "Featurette", "site": "YouTube", "key": "feat", "type": "Featurette"},
				map[string]any{"name": "Official Trailer", "site": "YouTube", "key": "trail", "type": "Trailer", "official": true},
			}},
			"release_dates": map[string]any{"results": []any{
				map[string]any{"iso_3166_1": "US", "release_dates": []any{map[string]any{"certification": "R"}}},
				map[string]any{"iso_3166_1": "GB", "release_dates": []any{
					map[string]any{"certification": ""},
					map[string]any{"certification": "15"},
				}},
			}},
			// TMDB keys availability by country and splits offers by how you get
			// the title rather than tagging each one. The slash in the key is
			// TMDB's, not a typo.
			"watch/providers": map[string]any{"results": map[string]any{
				"GB": map[string]any{
					"link": "https://www.themoviedb.org/movie/335984/watch?locale=GB",
					"flatrate": []any{
						map[string]any{"provider_name": "Netflix", "logo_path": "/netflix.jpg", "display_priority": 1},
						map[string]any{"provider_name": "Prime Video", "logo_path": "/prime.jpg", "display_priority": 0},
					},
					// A service commonly appears under several types; the better
					// terms should win and the duplicate should not be shown twice.
					"rent": []any{
						map[string]any{"provider_name": "Apple TV", "logo_path": "/apple.jpg", "display_priority": 0},
						map[string]any{"provider_name": "Netflix", "logo_path": "/netflix.jpg", "display_priority": 5},
					},
					"buy": []any{
						map[string]any{"provider_name": "Apple TV", "logo_path": "/apple.jpg", "display_priority": 0},
					},
				},
				"US": map[string]any{
					"link":     "https://www.themoviedb.org/movie/335984/watch?locale=US",
					"flatrate": []any{map[string]any{"provider_name": "Max", "logo_path": "/max.jpg"}},
				},
				// A region TMDB has a page for but nothing carries the title in.
				"IE": map[string]any{"link": "https://www.themoviedb.org/movie/335984/watch?locale=IE"},
			}},
		})
	})

	// The franchise. The detail carries only its id and name; the members cost a
	// second request, and TMDB returns them in popularity rather than release
	// order.
	mux.HandleFunc("/3/collection/344", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"name": "Blade Runner Collection", "overview": "A dystopian franchise.",
			"poster_path": "/coll.jpg", "backdrop_path": "/collback.jpg",
			"parts": []any{
				map[string]any{"id": 335984, "title": "Blade Runner 2049", "release_date": "2017-10-04", "poster_path": "/poster.jpg"},
				map[string]any{"id": 78, "title": "Blade Runner", "release_date": "1982-06-25", "poster_path": "/br.jpg"},
			},
		})
	})

	// The reverse lookup: an IMDb id to TMDB's own.
	mux.HandleFunc("/3/find/tt1856101", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("external_source") != "imdb_id" {
			http.Error(w, "external_source is required", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"movie_results": []any{map[string]any{"id": 335984, "title": "Blade Runner 2049", "release_date": "2017-10-04"}},
		})
	})
	mux.HandleFunc("/3/find/tt0000000", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"movie_results": []any{}, "tv_results": []any{}})
	})

	mux.HandleFunc("/3/configuration", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"images": map[string]any{
			"secure_base_url": "https://fake-cdn.example/t/p/",
			"poster_sizes":    []any{"w92", "w500", "original"},
			"backdrop_sizes":  []any{"w1280", "original"},
			"logo_sizes":      []any{"w500", "original"},
			"profile_sizes":   []any{"w185", "original"},
			"still_sizes":     []any{"w300", "original"},
		}})
	})

	// Discover, backing user-defined catalogs. It asserts the module's own
	// parameters won rather than the user's.
	discover := func(body map[string]any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if q.Get("api_key") == "attacker" {
				http.Error(w, "a user query overrode the credential", http.StatusForbidden)
				return
			}
			if q.Get("page") != "1" {
				http.Error(w, "the module must own paging, got page="+q.Get("page"), http.StatusBadRequest)
				return
			}
			writeJSON(w, body)
		}
	}
	mux.HandleFunc("/3/discover/movie", discover(map[string]any{"results": []any{
		map[string]any{"id": 335984, "title": "Blade Runner 2049", "release_date": "2017-10-04", "poster_path": "/poster.jpg"},
	}}))
	mux.HandleFunc("/3/discover/tv", discover(map[string]any{"results": []any{
		map[string]any{"id": 1396, "name": "Breaking Bad", "first_air_date": "2008-01-20", "poster_path": "/bb.jpg"},
	}}))

	mux.HandleFunc("/3/tv/1396", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"id": 1396, "name": "Breaking Bad",
			"overview":         "A chemistry teacher turns to manufacturing.",
			"first_air_date":   "2008-01-20",
			"poster_path":      "/bb.jpg",
			"vote_average":     8.9,
			"episode_run_time": []any{49},
			"genres":           []any{map[string]any{"name": "Drama"}},
			"seasons": []any{
				// Season 0 is TMDB's specials; season 3 is declared with no
				// episodes and must not cost a request.
				map[string]any{"season_number": 0, "episode_count": 1},
				map[string]any{"season_number": 1, "episode_count": 2},
				map[string]any{"season_number": 2, "episode_count": 1},
				map[string]any{"season_number": 3, "episode_count": 0},
			},
			"credits": map[string]any{"cast": []any{map[string]any{"name": "Bryan Cranston", "character": "Walter White", "profile_path": "/bryan.jpg", "order": 0}}},
			"images":  map[string]any{"logos": []any{}},
			// TVDB reports television only, which is why a film's external ids
			// above carry no tvdb_id.
			"external_ids": map[string]any{"imdb_id": "tt0903747", "tvdb_id": 81189},
			"keywords":     map[string]any{"results": []any{map[string]any{"name": "drug cartel"}}},
			"content_ratings": map[string]any{"results": []any{
				map[string]any{"iso_3166_1": "US", "rating": "TV-MA"},
				map[string]any{"iso_3166_1": "GB", "rating": "18"},
			}},
		})
	})

	mux.HandleFunc("/3/tv/1396/season/0", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"episodes": []any{
			map[string]any{"episode_number": 1, "name": "Good Cop Bad Cop", "overview": "A special.", "still_path": "/s0e1.jpg", "air_date": "2009-02-17"},
		}})
	})
	mux.HandleFunc("/3/tv/1396/season/1", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"episodes": []any{
			map[string]any{"episode_number": 2, "name": "Cat's in the Bag...", "overview": "Second.", "still_path": "/s1e2.jpg", "air_date": "2008-01-27"},
			map[string]any{"episode_number": 1, "name": "Pilot", "overview": "First.", "still_path": "/s1e1.jpg", "air_date": "2008-01-20"},
		}})
	})
	mux.HandleFunc("/3/tv/1396/season/2", func(w http.ResponseWriter, r *http.Request) {
		// An untitled episode, which must fall back to its number rather than
		// materialising a node with an empty title.
		writeJSON(w, map[string]any{"episodes": []any{
			map[string]any{"episode_number": 1, "name": "", "overview": "", "still_path": "", "air_date": ""},
		}})
	})
	mux.HandleFunc("/3/tv/1396/season/3", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "season 3 must never be requested: it declares no episodes", http.StatusTeapot)
	})

	mux.HandleFunc("/3/trending/movie/week", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"results": []any{
			// No media_type, as a list endpoint returns: the type comes from the
			// catalog declaration.
			map[string]any{"id": 335984, "title": "Blade Runner 2049", "release_date": "2017-10-04", "poster_path": "/poster.jpg"},
		}})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"status_code": 34, "status_message": "The resource you requested could not be found: " + r.URL.Path})
	})

	return httptest.NewServer(authGate(mux))
}

// authGate rejects a request carrying neither credential with TMDB's own 401,
// so the module's no-key and bad-key paths are exercised against the real
// failure shape rather than an invented one.
func authGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hasKey := r.URL.Query().Get("api_key") != ""
		hasBearer := strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !hasKey && !hasBearer {
			w.WriteHeader(http.StatusUnauthorized)
			writeJSON(w, map[string]any{"status_code": 7, "status_message": "Invalid API key: You must be granted a valid key."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

// keySettings is a settings document with a v3 API key.
func keySettings() []byte {
	return []byte(`{"apiKey":"0123456789abcdef0123456789abcdef"}`)
}

func movieRef(id string) v1.ContentRef {
	return v1.ContentRef{
		Provider: "tmdb", NativeID: id, NativeType: "movie",
		MediaType: v1.MediaMovie, ExternalScheme: "tmdb", ExternalID: id,
	}
}

func seriesRef(id string) v1.ContentRef {
	return v1.ContentRef{
		Provider: "tmdb", NativeID: id, NativeType: "tv",
		MediaType: v1.MediaTVSeries, ExternalScheme: "tmdb", ExternalID: id,
	}
}

// fakeContent is a minimal, faithful v1.ContentService: it assigns ids, keeps
// nodes in memory, inherits a child's work id and media type from its parent as
// the real service does, and resolves FindContentByExternalID against stored
// works — enough to exercise the capability including cross-scheme dedup.
type fakeContent struct {
	seq   int
	nodes map[v1.NodeID]v1.Node
	order []v1.NodeID
	parts []v1.Part
	binds []v1.BindContentSourceCommand
}

func newFakeContent() *fakeContent {
	return &fakeContent{nodes: make(map[v1.NodeID]v1.Node)}
}

func (f *fakeContent) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

func (f *fakeContent) put(n v1.Node) {
	f.nodes[n.ID] = n
	f.order = append(f.order, n.ID)
}

// childrenOf returns a node's children in creation order, which is the order the
// capability wrote them.
func (f *fakeContent) childrenOf(parent v1.NodeID) []v1.Node {
	var out []v1.Node
	for _, id := range f.order {
		n := f.nodes[id]
		if n.ParentID != nil && *n.ParentID == parent {
			out = append(out, n)
		}
	}
	return out
}

func (f *fakeContent) AddContentWork(_ context.Context, cmd v1.AddContentWorkCommand) (v1.AddContentWorkResult, error) {
	id := v1.NodeID(f.nextID("work"))
	n := v1.Node{
		ID: id, WorkID: id, Kind: v1.NodeWork,
		MediaType: cmd.MediaType, Title: cmd.Title, Status: v1.NodeActive,
		ExternalIDs: cmd.ExternalIDs, Artwork: cmd.Artwork,
	}
	f.put(n)
	return v1.AddContentWorkResult{Work: n}, nil
}

func (f *fakeContent) AddContentChild(_ context.Context, cmd v1.AddContentChildCommand) (v1.AddContentChildResult, error) {
	parent := f.nodes[cmd.ParentID]
	id := v1.NodeID(f.nextID("node"))
	parentID := cmd.ParentID
	n := v1.Node{
		ID: id, WorkID: parent.WorkID, ParentID: &parentID,
		Kind: cmd.Kind, MediaType: parent.MediaType,
		ContainerType: cmd.ContainerType, ItemType: cmd.ItemType,
		Title: cmd.Title, NaturalOrder: cmd.NaturalOrder, Status: v1.NodeActive,
		Artwork: cmd.Artwork,
	}
	f.put(n)
	return v1.AddContentChildResult{Node: n}, nil
}

func (f *fakeContent) AttachContentPart(_ context.Context, cmd v1.AttachContentPartCommand) (v1.AttachContentPartResult, error) {
	p := v1.Part{
		ID: v1.PartID(f.nextID("part")), NodeID: cmd.NodeID,
		Role: cmd.Role, Location: cmd.Location,
	}
	f.parts = append(f.parts, p)
	return v1.AttachContentPartResult{Part: p}, nil
}

func (f *fakeContent) BindContentSource(_ context.Context, cmd v1.BindContentSourceCommand) (v1.BindContentSourceResult, error) {
	f.binds = append(f.binds, cmd)
	b := v1.SourceBinding{
		ID: v1.SourceBindingID(f.nextID("bind")), NodeID: cmd.NodeID,
		SourceProvider: cmd.SourceProvider, SourceRef: cmd.SourceRef, Status: cmd.Status,
	}
	return v1.BindContentSourceResult{Binding: b}, nil
}

func (f *fakeContent) FindContentByExternalID(_ context.Context, q v1.FindContentByExternalIDQuery) (v1.FindContentByExternalIDResult, error) {
	var out []v1.Node
	for _, id := range f.order {
		n := f.nodes[id]
		if !n.IsRoot() || len(n.ExternalIDs) == 0 {
			continue
		}
		ids := map[string]string{}
		if err := json.Unmarshal(n.ExternalIDs, &ids); err != nil {
			continue
		}
		if q.Value != "" && ids[q.Scheme] == q.Value {
			out = append(out, n)
		}
	}
	return v1.FindContentByExternalIDResult{Nodes: out}, nil
}

// The remaining ContentService methods are not exercised by this capability;
// they are stubbed to satisfy the interface.

func (f *fakeContent) SearchContent(context.Context, v1.SearchContentQuery) (v1.SearchContentResult, error) {
	return v1.SearchContentResult{}, nil
}
func (f *fakeContent) GetContentNode(context.Context, v1.GetContentNodeQuery) (v1.GetContentNodeResult, error) {
	return v1.GetContentNodeResult{}, nil
}
func (f *fakeContent) ListContentParts(context.Context, v1.ListContentPartsQuery) (v1.ListContentPartsResult, error) {
	return v1.ListContentPartsResult{}, nil
}
func (f *fakeContent) RelateContent(context.Context, v1.RelateContentCommand) (v1.RelateContentResult, error) {
	return v1.RelateContentResult{}, nil
}
func (f *fakeContent) ResolveContentBinding(context.Context, v1.ResolveContentBindingCommand) (v1.ResolveContentBindingResult, error) {
	return v1.ResolveContentBindingResult{}, nil
}
func (f *fakeContent) RecordPlaybackProgress(context.Context, v1.RecordPlaybackProgressCommand) (v1.RecordPlaybackProgressResult, error) {
	return v1.RecordPlaybackProgressResult{}, nil
}
func (f *fakeContent) SetPlaybackFinished(context.Context, v1.SetPlaybackFinishedCommand) (v1.SetPlaybackFinishedResult, error) {
	return v1.SetPlaybackFinishedResult{}, nil
}
func (f *fakeContent) GetPlaybackState(context.Context, v1.GetPlaybackStateQuery) (v1.GetPlaybackStateResult, error) {
	return v1.GetPlaybackStateResult{}, nil
}
func (f *fakeContent) ListPlaybackStates(context.Context, v1.ListPlaybackStatesQuery) (v1.ListPlaybackStatesResult, error) {
	return v1.ListPlaybackStatesResult{}, nil
}
func (f *fakeContent) ListInProgress(context.Context, v1.ListInProgressQuery) (v1.ListInProgressResult, error) {
	return v1.ListInProgressResult{}, nil
}

var _ v1.ContentService = (*fakeContent)(nil)
