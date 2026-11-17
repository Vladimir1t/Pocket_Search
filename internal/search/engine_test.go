package search

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func memoryEngine(t testing.TB) *Engine {
	t.Helper()
	e, err := Open("")
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func put(t testing.TB, e *Engine, d Document) {
	t.Helper()
	if _, err := e.Put(d); err != nil {
		t.Fatal(err)
	}
}

func TestSearchRankingAndFiltering(t *testing.T) {
	e := memoryEngine(t)
	put(t, e, Document{ID: "a", Title: "Go engineer", Text: "remote", Category: "jobs"})
	put(t, e, Document{ID: "b", Title: "Engineer", Text: "Go remote", Category: "jobs"})
	put(t, e, Document{ID: "c", Title: "Go course", Text: "remote", Category: "courses"})
	put(t, e, Document{ID: "d", Title: "Go onsite", Category: "jobs"})
	r := e.Search("GO remote", "jobs", 1)
	if r.Total != 2 || len(r.Hits) != 1 || r.Hits[0].Document.ID != "a" {
		t.Fatalf("unexpected result: %+v", r)
	}
	full := e.Search("go remote", "jobs", 20)
	if full.Hits[0].Score <= full.Hits[1].Score {
		t.Fatal("title match should rank above text match")
	}
	duplicateQuery := e.Search("go go remote", "jobs", 20)
	if duplicateQuery.Hits[0].Score != full.Hits[0].Score {
		t.Fatal("repeated query terms must not change score")
	}
	if got := e.Search("nonexistent", "", 20); got.Total != 0 || got.Hits == nil {
		t.Fatalf("expected an empty JSON array: %+v", got)
	}
}

func TestUnicodeAndProgrammingLanguages(t *testing.T) {
	e := memoryEngine(t)
	put(t, e, Document{ID: "cpp", Title: "C++ разработчик", Text: "Удалённая работа"})
	put(t, e, Document{ID: "c", Title: "C разработчик"})
	r := e.Search("c++ УДАЛЕННАЯ", "", 20)
	if r.Total != 1 || r.Hits[0].Document.ID != "cpp" {
		t.Fatalf("unexpected tokenization: %+v", r)
	}
}

func TestStandalonePunctuationIsNotATerm(t *testing.T) {
	e := memoryEngine(t)
	put(t, e, Document{ID: "x", Title: "Q & A + more # notes", Text: "C# and C++"})
	for _, q := range []string{"+", "#", "++ ##"} {
		if QueryValid(q) {
			t.Errorf("query %q has no real terms and must be invalid", q)
		}
		if got := e.Search(q, "", 20); got.Total != 0 {
			t.Errorf("query %q matched %+v", q, got)
		}
	}
	if got := e.Search("c# c++", "", 20); got.Total != 1 {
		t.Fatalf("c# and c++ must stay searchable: %+v", got)
	}
}

func TestUpdateAndDeleteRemoveStalePostings(t *testing.T) {
	e := memoryEngine(t)
	put(t, e, Document{ID: "one", Title: "Python"})
	created, err := e.Put(Document{ID: "one", Title: "Go"})
	if err != nil || created {
		t.Fatalf("replacement: created=%v err=%v", created, err)
	}
	if e.Search("python", "", 20).Total != 0 || e.Search("go", "", 20).Total != 1 {
		t.Fatal("index did not reflect replacement")
	}
	if err := e.Delete("one"); err != nil {
		t.Fatal(err)
	}
	if e.Stats() != (Stats{}) || e.Search("go", "", 20).Total != 0 {
		t.Fatal("delete left stale index entries")
	}
	if err := e.Delete("one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestStableTieBreak(t *testing.T) {
	e := memoryEngine(t)
	put(t, e, Document{ID: "b", Title: "Go"})
	put(t, e, Document{ID: "a", Title: "Go"})
	for range 20 {
		if e.Search("go", "", 1).Hits[0].Document.ID != "a" {
			t.Fatal("ties must be ordered by document id")
		}
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "documents.json")
	e, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	put(t, e, Document{ID: "keep", Title: "Old"})
	put(t, e, Document{ID: "keep", Title: "Go"})
	put(t, e, Document{ID: "remove", Title: "Python"})
	if err := e.Delete("remove"); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Stats().Documents != 1 || reloaded.Search("go", "", 20).Total != 1 || reloaded.Search("old", "", 20).Total != 0 {
		t.Fatal("reloaded state does not match committed state")
	}
}

func TestFailedPersistenceDoesNotPublishChanges(t *testing.T) {
	e := memoryEngine(t)
	put(t, e, Document{ID: "one", Title: "Original"})
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	e.path = filepath.Join(blocker, "documents.json")
	if _, err := e.Put(Document{ID: "one", Title: "Replacement"}); err == nil {
		t.Fatal("expected a persistence error")
	}
	if err := e.Delete("one"); err == nil {
		t.Fatal("expected a delete persistence error")
	}
	if e.Search("original", "", 20).Total != 1 || e.Search("replacement", "", 20).Total != 0 {
		t.Fatal("failed writes changed visible state")
	}
}

func TestRejectCorruptSnapshot(t *testing.T) {
	for _, data := range []string{
		`{"version":1,"documents":[`,
		`{"version":2,"documents":[]}`,
		`{"version":1,"documents":[]} {}`,
		`{"version":1,"documents":[{"id":"x","title":"A"},{"id":"x","title":"B"}]}`,
	} {
		path := filepath.Join(t.TempDir(), "documents.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil {
			t.Fatalf("accepted invalid snapshot %s", data)
		}
	}
}

func TestConcurrentReadersAndWriters(t *testing.T) {
	e := memoryEngine(t)
	var wg sync.WaitGroup
	for worker := range 8 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			id := fmt.Sprintf("worker-%d", worker)
			for range 100 {
				if _, err := e.Put(Document{ID: id, Title: "Go engineer"}); err != nil {
					t.Error(err)
					return
				}
				e.Search("go", "", 20)
				e.Get(id)
				e.Stats()
				if err := e.Delete(id); err != nil {
					t.Error(err)
					return
				}
			}
		}(worker)
	}
	wg.Wait()
	if e.Stats() != (Stats{}) {
		t.Fatal("unexpected final state")
	}
}

func BenchmarkSearch(b *testing.B) {
	e := memoryEngine(b)
	for i := range 1000 {
		text := "backend databases networking"
		if i%20 == 0 {
			text += " remote"
		}
		put(b, e, Document{ID: fmt.Sprint(i), Title: "Go engineer", Text: text})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		e.Search("go remote", "", 20)
	}
}
