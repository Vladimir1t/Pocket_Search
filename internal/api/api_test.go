package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pocketsearch/internal/search"
)

func request(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestDocumentLifecycle(t *testing.T) {
	e, err := search.Open("")
	if err != nil {
		t.Fatal(err)
	}
	h := New(e)
	body := `{"title":"Go developer","text":"remote","category":"jobs"}`
	for _, want := range []int{http.StatusCreated, http.StatusOK} {
		w := request(h, "PUT", "/documents/job-1", body)
		if w.Code != want {
			t.Fatalf("put: %d %s", w.Code, w.Body.String())
		}
	}
	w := request(h, "GET", "/search?q=go+remote&category=jobs&limit=1", "")
	var result search.Result
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || result.Total != 1 || result.Hits[0].Document.ID != "job-1" {
		t.Fatalf("search: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/documents/job-1", ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := request(h, "DELETE", "/documents/job-1", ""); w.Code != 204 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/documents/job-1", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/search?q=go", ""); !strings.Contains(w.Body.String(), `"hits":[]`) {
		t.Fatal(w.Body.String())
	}
}

func TestRejectInvalidRequests(t *testing.T) {
	e, _ := search.Open("")
	h := New(e)
	tests := []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/search", "", 400},
		{"GET", "/search?q=go&limit=0", "", 400},
		{"GET", "/search?q=go&limit=101", "", 400},
		{"GET", "/search?q=go&limit=nope", "", 400},
		{"GET", "/search?q=%2B", "", 400},
		{"PUT", "/documents/bad.id", `{"title":"Go"}`, 400},
		{"PUT", "/documents/" + strings.Repeat("a", 65), `{"title":"Go"}`, 400},
		{"PUT", "/documents/x", `{"title":""}`, 400},
		{"PUT", "/documents/x", `{"title":"Go","unknown":true}`, 400},
		{"PUT", "/documents/x", `{"title":"Go"} {}`, 400},
		{"PUT", "/documents/x", `{"title":"` + strings.Repeat("x", 70_000) + `"}`, 413},
		{"DELETE", "/documents/missing", "", 404},
		{"POST", "/documents/x", "", 405},
	}
	for _, tt := range tests {
		w := request(h, tt.method, tt.path, tt.body)
		if w.Code != tt.status {
			t.Errorf("%s %s: got %d want %d: %s", tt.method, tt.path, w.Code, tt.status, w.Body.String())
		}
	}
	if e.Stats().Documents != 0 {
		t.Fatal("invalid requests mutated the store")
	}
}

func TestHealthAndStats(t *testing.T) {
	e, _ := search.Open("")
	h := New(e)
	if w := request(h, "GET", "/healthz", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ok"`) {
		t.Fatalf("healthz: %d %s", w.Code, w.Body.String())
	}
	request(h, "PUT", "/documents/a", `{"title":"Go remote"}`)
	w := request(h, "GET", "/stats", "")
	var stats search.Stats
	if err := json.Unmarshal(w.Body.Bytes(), &stats); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || stats.Documents != 1 || stats.Terms != 2 {
		t.Fatalf("stats: %d %+v", w.Code, stats)
	}
}

func TestPersistenceFailureReturns500AndKeepsState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	path := filepath.Join(dir, "documents.json")
	e, err := search.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	h := New(e)
	if w := request(h, "PUT", "/documents/a", `{"title":"Original"}`); w.Code != http.StatusCreated {
		t.Fatalf("setup: %d %s", w.Code, w.Body.String())
	}
	// Replace the data directory with a regular file so every later write fails.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	if w := request(h, "PUT", "/documents/a", `{"title":"Replacement"}`); w.Code != http.StatusInternalServerError {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "DELETE", "/documents/a", ""); w.Code != http.StatusInternalServerError {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := request(h, "GET", "/search?q=original", ""); !strings.Contains(w.Body.String(), `"total":1`) {
		t.Fatalf("state changed after failed writes: %s", w.Body.String())
	}
	if w := request(h, "GET", "/search?q=replacement", ""); !strings.Contains(w.Body.String(), `"total":0`) {
		t.Fatalf("failed write became visible: %s", w.Body.String())
	}
}
