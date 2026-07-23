package tmdb_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestModuleImportsOnlyPublishedContracts is the module boundary made
// executable: this module must use only the published *contract* modules — the
// SDK, and the shared SDUI contract it authors its settings screen with
// (ADR 0038) — plus the standard library.
//
// It matters more here than in an extension module, not less. This is a **core**
// module (ADR 0062), compiled into the Platform binary by Mosaic's CI and
// sharing its address space — the tier where a private Platform import would
// actually resolve if the two ever lived in one repository. Keeping it a
// separate Go module means Go itself rejects that; this parse keeps the intent
// explicit and catches a third-party dependency creeping into a binary whose
// dependency graph is shared with every other core module.
func TestModuleImportsOnlyPublishedContracts(t *testing.T) {
	const (
		sdkPrefix      = "github.com/mosaic-media/sdk/"
		sduiPrefix     = "github.com/mosaic-media/sdui/"
		platformPrefix = "github.com/mosaic-media/platform/"
	)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++

		file, err := parser.ParseFile(fset, filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", name, err)
			}
			switch {
			// Standard-library imports have no dot in their first segment.
			case !strings.Contains(strings.SplitN(path, "/", 2)[0], "."):
			case strings.HasPrefix(path, sdkPrefix):
				// The published SDK — the primary contract a module builds against.
			case strings.HasPrefix(path, sduiPrefix):
				// The shared SDUI contract — a module builds its own settings UI
				// with the producer binding (ADR 0038, ADR 0025).
			case strings.HasPrefix(path, platformPrefix):
				t.Errorf("%s imports private Platform package %q; a module may import only the SDK", name, path)
			default:
				t.Errorf("%s imports third-party package %q; this module may use only the published contracts and the standard library", name, path)
			}
		}
	}

	if checked == 0 {
		t.Fatal("no non-test source files were checked; the boundary test is not looking at anything")
	}
}
