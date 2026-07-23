//go:build linkercheck

package tmdb

import "testing"

// The linker-injection guard, behind a build tag so it runs only in the pass
// that supplies the flag.
//
// **`-X` on a path that does not resolve is silently ignored.** Rename the
// variable, move it to another file's package, or mistype the module path in
// the release workflow, and the build still succeeds — it just links nothing,
// and every deployment that relied on the bundled token loses metadata with no
// error anywhere. That is the one failure this whole mechanism can have and it
// reports itself nowhere, so it is checked here rather than trusted.
//
// The container gate runs the suite twice: once normally, and once as
//
//	go test -tags linkercheck \
//	  -ldflags "-X github.com/mosaic-media/module-tmdb.defaultReadAccessToken=$canary" \
//	  -run TestLinkerInjectionPathResolves ./...
//
// so the symbol path in the release build is verified by the same string that
// appears in this repository's own gate.
func TestLinkerInjectionPathResolves(t *testing.T) {
	const canary = "linker-injection-canary"
	if defaultReadAccessToken != canary {
		t.Fatalf("defaultReadAccessToken = %q, want %q — the -X symbol path no longer resolves, "+
			"so a release build would link no token and fail silently", defaultReadAccessToken, canary)
	}
	// And the resolution actually uses it, not merely stores it.
	token, bundled, ok := resolveToken(settings{})
	if !ok || !bundled || token != canary {
		t.Fatalf("resolveToken with a linked-in token = %q bundled=%v ok=%v", token, bundled, ok)
	}
}
