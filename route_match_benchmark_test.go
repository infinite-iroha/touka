package touka

import "testing"

var (
	benchmarkRouteHandlers  HandlersChain
	benchmarkRouteFullPath  string
	benchmarkRouteParamsLen int
	benchmarkRouteCIPath    []byte
	benchmarkRouteCIFound   bool
)

func buildRouteMatchBenchmarkTree() *node {
	tree := &node{}
	routes := []string{
		"/",
		"/health",
		"/contact",
		"/api/v1/users",
		"/api/v1/users/:id",
		"/api/v1/users/:id/settings",
		"/assets/*filepath",
		"/abc/b",
		"/abc/:p1/cde",
		"/abc/:p1/:p2/def/*filepath",
	}

	for _, route := range routes {
		tree.addRoute(route, fakeHandler(route))
	}

	return tree
}

func benchmarkRouteLookup(b *testing.B, tree *node, path string, wantFullPath string) {
	b.Helper()

	params := make(Params, 0, 4)
	skipped := make([]skippedNode, 0, 8)

	value := tree.getValue(path, &params, &skipped, true)
	if wantFullPath == "" {
		if value.handlers != nil {
			b.Fatalf("expected no match for %q, got %q", path, value.fullPath)
		}
	} else {
		if value.handlers == nil {
			b.Fatalf("expected match for %q, got nil handlers", path)
		}
		if value.fullPath != wantFullPath {
			b.Fatalf("expected full path %q for %q, got %q", wantFullPath, path, value.fullPath)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		params = params[:0]
		skipped = skipped[:0]
		value = tree.getValue(path, &params, &skipped, true)
	}

	benchmarkRouteHandlers = value.handlers
	benchmarkRouteFullPath = value.fullPath
	if value.params != nil {
		benchmarkRouteParamsLen = len(*value.params)
	} else {
		benchmarkRouteParamsLen = 0
	}
}

func BenchmarkRouteMatch(b *testing.B) {
	tree := buildRouteMatchBenchmarkTree()

	b.Run("StaticHit", func(b *testing.B) {
		benchmarkRouteLookup(b, tree, "/api/v1/users", "/api/v1/users")
	})

	b.Run("ParamHit", func(b *testing.B) {
		benchmarkRouteLookup(b, tree, "/api/v1/users/123", "/api/v1/users/:id")
	})

	b.Run("BacktrackingHit", func(b *testing.B) {
		benchmarkRouteLookup(b, tree, "/abc/b/d/def/some/file.txt", "/abc/:p1/:p2/def/*filepath")
	})

	b.Run("Miss", func(b *testing.B) {
		benchmarkRouteLookup(b, tree, "/does/not/exist", "")
	})

	b.Run("CaseInsensitiveHit", func(b *testing.B) {
		path := "/API/V1/USERS/123/SETTINGS"
		out, found := tree.findCaseInsensitivePath(path, true)
		if !found {
			b.Fatalf("expected fixed-path match for %q", path)
		}
		if got := string(out); got != "/api/v1/users/123/settings" {
			b.Fatalf("expected fixed-path result %q, got %q", "/api/v1/users/123/settings", got)
		}

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			out, found = tree.findCaseInsensitivePath(path, true)
		}

		benchmarkRouteCIPath = out
		benchmarkRouteCIFound = found
	})

	b.Run("CaseInsensitiveMiss", func(b *testing.B) {
		path := "/DOES/NOT/EXIST"
		out, found := tree.findCaseInsensitivePath(path, true)
		if found || out != nil {
			b.Fatalf("expected no fixed-path match for %q, got %q, %t", path, string(out), found)
		}

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			out, found = tree.findCaseInsensitivePath(path, true)
		}

		benchmarkRouteCIPath = out
		benchmarkRouteCIFound = found
	})
}
