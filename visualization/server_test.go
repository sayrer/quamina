package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	quamina "quamina.net/go/quamina/v2"
)

// buildTestServer creates a server with a handful of words for use in tests.
// It does NOT bind a real port.
func buildTestServer(t *testing.T) *server {
	t.Helper()
	words := []string{"crane", "adieu", "raise"}

	q, err := quamina.New()
	if err != nil {
		t.Fatalf("quamina.New: %v", err)
	}
	for i, w := range words {
		pat := fmt.Sprintf(`{"x": [ {"shellstyle": "*%s*"} ] }`, w)
		if err := q.AddPattern(fmt.Sprintf("p%d", i), pat); err != nil {
			t.Fatalf("AddPattern(%q): %v", w, err)
		}
	}
	exp, err := quamina.NewVizExporter(q, "x")
	if err != nil {
		t.Fatalf("NewVizExporter: %v", err)
	}
	nfa := exp.NFA()
	return &server{exporter: exp, nfaCache: &nfa, words: words}
}

func TestGetNFA(t *testing.T) {
	srv := buildTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/nfa", nil)
	rec := httptest.NewRecorder()
	srv.handleNFA(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var nfa quamina.VizNFA
	if err := json.NewDecoder(rec.Body).Decode(&nfa); err != nil {
		t.Fatalf("decode NFA: %v", err)
	}
	if len(nfa.Nodes) == 0 {
		t.Fatal("expected NFA to have nodes")
	}
	if len(nfa.Edges) == 0 {
		t.Fatal("expected NFA to have edges")
	}
}

func TestPostFeed(t *testing.T) {
	srv := buildTestServer(t)

	body := bytes.NewBufferString(`{"word":"crane"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/feed", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.handleFeed(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	var feed quamina.VizFeed
	if err := json.NewDecoder(rec.Body).Decode(&feed); err != nil {
		t.Fatalf("decode VizFeed: %v", err)
	}
	if len(feed.Matches) == 0 {
		t.Fatal("expected at least one match for 'crane' (pattern p0 = *crane*)")
	}
	if len(feed.DFAStates) == 0 {
		t.Fatal("expected lazy-DFA states to be materialized after feed")
	}
}

func TestGetWords(t *testing.T) {
	srv := buildTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/words", nil)
	rec := httptest.NewRecorder()
	srv.handleWords(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	var words []string
	if err := json.NewDecoder(rec.Body).Decode(&words); err != nil {
		t.Fatalf("decode words: %v", err)
	}
	if len(words) == 0 {
		t.Fatal("expected non-empty word list")
	}
}
