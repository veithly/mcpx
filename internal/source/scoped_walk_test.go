package source

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func scopedWalkFixture(t testing.TB, count int) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"wanted/nested", "unrelated/deep"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "wanted/nested/target.go"), []byte("needle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("unrelated/deep/file-%04d.go", i)), []byte("needle\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestScopedSearchDoesNotVisitUnrelatedFiles(t *testing.T) {
	root := scopedWalkFixture(t, 100)
	for _, mode := range []string{"search", "context"} {
		t.Run(mode, func(t *testing.T) {
			visited := 0
			allowed := func(path string) bool { visited++; return true }
			if mode == "search" {
				result, err := SearchWith(root, SearchOptions{Query: "needle", ScopePaths: []string{"wanted"}}, allowed)
				if err != nil || len(result.Matches) != 1 || result.Matches[0].Path != "wanted/nested/target.go" {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			} else {
				result, err := SmartQueryPage(root, SmartQueryOptions{Query: "needle", Mode: "exact", ScopePaths: []string{"wanted"}, Allowed: allowed})
				if err != nil || len(result["files"].([]map[string]any)) != 1 {
					t.Fatalf("result=%+v error=%v", result, err)
				}
			}
			if visited != 1 {
				t.Fatalf("scoped %s visited %d files, want only the one in scope", mode, visited)
			}
		})
	}
}

func BenchmarkScopedSearchLargeUnrelatedTree(b *testing.B) {
	root := scopedWalkFixture(b, 2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := SearchWith(root, SearchOptions{Query: "needle", ScopePaths: []string{"wanted"}}, nil)
		if err != nil || len(result.Matches) != 1 {
			b.Fatalf("unexpected search result: %v", err)
		}
	}
}

func TestScopedWalkPreservesBoundariesAndPolicy(t *testing.T) {
	root := scopedWalkFixture(t, 1)
	for _, tc := range []struct {
		name   string
		scopes []string
		deny   bool
		want   int
	}{
		{"nested directory", []string{"wanted/nested"}, false, 1},
		{"exact file", []string{"wanted/nested/target.go"}, false, 1},
		{"overlap", []string{"wanted", "wanted/nested"}, false, 1},
		{"explicit root", []string{"."}, false, 2},
		{"missing", []string{"missing"}, false, 0},
		{"escape", []string{"../outside"}, false, 0},
		{"denied", []string{"wanted"}, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := SearchWith(root, SearchOptions{Query: "needle", ScopePaths: tc.scopes}, func(string) bool { return !tc.deny })
			if err != nil || len(result.Matches) != tc.want {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}
