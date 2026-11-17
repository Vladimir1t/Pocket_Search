package search

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

var ErrNotFound = errors.New("document not found")

type Document struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	Category string `json:"category"`
}

func (d Document) Validate() error {
	if len(d.ID) == 0 || len(d.ID) > 64 {
		return errors.New("id must contain 1–64 ASCII letters, digits, hyphens or underscores")
	}
	for _, r := range d.ID {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return errors.New("invalid document id")
		}
	}
	if !utf8.ValidString(d.Title) || !utf8.ValidString(d.Text) || !utf8.ValidString(d.Category) {
		return errors.New("document must be valid UTF-8")
	}
	if strings.TrimSpace(d.Title) == "" || len(d.Title) > 512 || len(d.Text) > 32_768 || len(d.Category) > 128 {
		return errors.New("title is required; maximum sizes in bytes: title=512, text=32768, category=128")
	}
	return nil
}

type Hit struct {
	Document Document `json:"document"`
	Score    float64  `json:"score"`
}

type Result struct {
	Total int   `json:"total"`
	Hits  []Hit `json:"hits"`
}

type Stats struct {
	Documents int `json:"documents"`
	Terms     int `json:"terms"`
}

type snapshot struct {
	Version   int        `json:"version"`
	Documents []Document `json:"documents"`
}

// Engine owns documents and their index. Use one Engine/process per snapshot file.
type Engine struct {
	mu       sync.RWMutex
	path     string
	docs     map[string]Document
	postings map[string]map[string]int
}

// Open rebuilds the index from a snapshot. An empty path selects in-memory storage.
func Open(path string) (*Engine, error) {
	e := &Engine{path: path, docs: make(map[string]Document), postings: make(map[string]map[string]int)}
	if path == "" {
		return e, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return e, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var s snapshot
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&s); err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("snapshot must contain exactly one JSON value")
	}
	if s.Version != 1 {
		return nil, fmt.Errorf("unsupported snapshot version %d", s.Version)
	}
	for _, d := range s.Documents {
		if err := d.Validate(); err != nil {
			return nil, fmt.Errorf("invalid stored document: %w", err)
		}
		if _, exists := e.docs[d.ID]; exists {
			return nil, fmt.Errorf("duplicate stored id %q", d.ID)
		}
		e.add(d)
	}
	return e, nil
}

// Put creates or replaces a document. Persistence failure leaves memory unchanged.
func (e *Engine) Put(d Document) (created bool, err error) {
	if err := d.Validate(); err != nil {
		return false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	old, exists := e.docs[d.ID]
	if exists && old == d {
		return false, nil
	}
	if err := e.persist(d.ID, &d); err != nil {
		return false, err
	}
	if exists {
		e.remove(old)
	}
	e.add(d)
	return !exists, nil
}

func (e *Engine) Delete(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	d, exists := e.docs[id]
	if !exists {
		return ErrNotFound
	}
	if err := e.persist(id, nil); err != nil {
		return err
	}
	e.remove(d)
	return nil
}

func (e *Engine) Get(id string) (Document, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d, ok := e.docs[id]
	return d, ok
}

func (e *Engine) Stats() Stats {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return Stats{Documents: len(e.docs), Terms: len(e.postings)}
}

// Search matches every unique query term. Category is an exact, case-sensitive filter.
func (e *Engine) Search(query, category string, limit int) Result {
	result := Result{Hits: []Hit{}}
	terms := uniqueTokens(query)
	if len(terms) == 0 || len(terms) > 16 || limit <= 0 {
		return result
	}
	if limit > 100 {
		limit = 100
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	// Start with the rarest term to avoid scanning every document.
	sort.Slice(terms, func(i, j int) bool {
		left, right := len(e.postings[terms[i]]), len(e.postings[terms[j]])
		if left == right {
			return terms[i] < terms[j]
		}
		return left < right
	})
	for id := range e.postings[terms[0]] {
		d := e.docs[id]
		if category != "" && d.Category != category {
			continue
		}
		score := 0.0
		match := true
		for _, term := range terms {
			frequency, exists := e.postings[term][id]
			if !exists {
				match = false
				break
			}
			idf := math.Log(1 + float64(len(e.docs))/float64(len(e.postings[term])))
			score += float64(frequency) * idf
		}
		if match {
			result.Hits = append(result.Hits, Hit{Document: d, Score: score})
		}
	}
	sort.Slice(result.Hits, func(i, j int) bool {
		if result.Hits[i].Score == result.Hits[j].Score {
			return result.Hits[i].Document.ID < result.Hits[j].Document.ID
		}
		return result.Hits[i].Score > result.Hits[j].Score
	})
	result.Total = len(result.Hits)
	if len(result.Hits) > limit {
		result.Hits = result.Hits[:limit]
	}
	return result
}

func QueryValid(q string) bool {
	n := len(uniqueTokens(q))
	return utf8.ValidString(q) && len(q) <= 512 && n > 0 && n <= 16
}

func tokens(text string) []string {
	text = strings.ReplaceAll(strings.ToLower(text), "ё", "е")
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '#'
	})
	// '+' and '#' are kept only inside words such as "c++" or "c#";
	// a standalone "+" or "#" is punctuation, not a searchable term.
	out := fields[:0]
	for _, field := range fields {
		if strings.IndexFunc(field, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) >= 0 {
			out = append(out, field)
		}
	}
	return out
}

func uniqueTokens(text string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, term := range tokens(text) {
		if !seen[term] {
			seen[term] = true
			out = append(out, term)
		}
	}
	return out
}

func frequencies(d Document) map[string]int {
	counts := make(map[string]int)
	for _, t := range tokens(d.Title) {
		counts[t] += 3
	}
	for _, t := range tokens(d.Text) {
		counts[t]++
	}
	return counts
}

func (e *Engine) add(d Document) {
	e.docs[d.ID] = d
	for term, count := range frequencies(d) {
		if e.postings[term] == nil {
			e.postings[term] = make(map[string]int)
		}
		e.postings[term][d.ID] = count
	}
}

func (e *Engine) remove(d Document) {
	delete(e.docs, d.ID)
	for term := range frequencies(d) {
		delete(e.postings[term], d.ID)
		if len(e.postings[term]) == 0 {
			delete(e.postings, term)
		}
	}
}

// persist writes a replacement snapshot before publishing the in-memory change.
// Rename protects against partial JSON on process failure, not all power-loss scenarios.
func (e *Engine) persist(id string, replacement *Document) error {
	if e.path == "" {
		return nil
	}
	s := snapshot{Version: 1, Documents: make([]Document, 0, len(e.docs)+1)}
	for key, d := range e.docs {
		if key != id {
			s.Documents = append(s.Documents, d)
		}
	}
	if replacement != nil {
		s.Documents = append(s.Documents, *replacement)
	}
	sort.Slice(s.Documents, func(i, j int) bool { return s.Documents[i].ID < s.Documents[j].ID })
	dir := filepath.Dir(e.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pocketsearch-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := json.NewEncoder(f).Encode(s); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), e.path)
}
