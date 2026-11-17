package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"pocketsearch/internal/search"
)

func New(engine *search.Engine) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		respond(w, http.StatusOK, engine.Stats())
	})
	mux.HandleFunc("PUT /documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Title    string `json:"title"`
			Text     string `json:"text"`
			Category string `json:"category"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil {
			var tooLarge *http.MaxBytesError
			status := http.StatusBadRequest
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			fail(w, status, "invalid or oversized JSON body")
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			fail(w, http.StatusBadRequest, "body must contain exactly one JSON object")
			return
		}
		d := search.Document{ID: r.PathValue("id"), Title: input.Title, Text: input.Text, Category: input.Category}
		if err := d.Validate(); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		created, err := engine.Put(d)
		if err != nil {
			slog.Error("persist document", "error", err)
			fail(w, http.StatusInternalServerError, "could not save document")
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		respond(w, status, d)
	})
	mux.HandleFunc("GET /documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := engine.Get(r.PathValue("id"))
		if !ok {
			fail(w, http.StatusNotFound, "document not found")
			return
		}
		respond(w, http.StatusOK, d)
	})
	mux.HandleFunc("DELETE /documents/{id}", func(w http.ResponseWriter, r *http.Request) {
		err := engine.Delete(r.PathValue("id"))
		if errors.Is(err, search.ErrNotFound) {
			fail(w, http.StatusNotFound, "document not found")
			return
		}
		if err != nil {
			slog.Error("delete document", "error", err)
			fail(w, http.StatusInternalServerError, "could not delete document")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if !search.QueryValid(q.Get("q")) {
			fail(w, http.StatusBadRequest, "q must contain 1–16 unique terms and at most 512 bytes")
			return
		}
		limit := 20
		if q.Has("limit") {
			var err error
			limit, err = strconv.Atoi(q.Get("limit"))
			if err != nil || limit < 1 || limit > 100 {
				fail(w, http.StatusBadRequest, "limit must be between 1 and 100")
				return
			}
		}
		respond(w, http.StatusOK, engine.Search(q.Get("q"), q.Get("category"), limit))
	})
	return mux
}

func fail(w http.ResponseWriter, status int, message string) {
	respond(w, status, map[string]string{"error": message})
}

func respond(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("write response", "error", err)
	}
}
