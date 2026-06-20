// Package main is the visualization server for Quamina's NFA / lazy-DFA demo.
// Run: go run ./visualization -words 12 -addr :8080
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"

	quamina "quamina.net/go/quamina/v2"
)

// server holds the shared state for the HTTP handlers.
type server struct {
	mu       sync.Mutex
	exporter *quamina.VizExporter
	nfaCache *quamina.VizNFA // computed once at startup
	words    []string
}

// feedRequest is the JSON body for POST /api/feed.
type feedRequest struct {
	Word string `json:"word"`
}

func main() {
	wordsN := flag.Int("words", 500, "number of words to sample (evenly across the alphabet) from testdata/wwords.txt")
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	words, err := loadWords("testdata/wwords.txt", *wordsN)
	if err != nil {
		log.Fatalf("loading words: %v", err)
	}

	q, err := quamina.New()
	if err != nil {
		log.Fatalf("quamina.New: %v", err)
	}
	for i, w := range words {
		pat := fmt.Sprintf(`{"x": [ {"shellstyle": "*%s*"} ] }`, w)
		if err := q.AddPattern(fmt.Sprintf("p%d", i), pat); err != nil {
			log.Fatalf("AddPattern(%q): %v", w, err)
		}
	}

	exp, err := quamina.NewVizExporter(q, "x")
	if err != nil {
		log.Fatalf("NewVizExporter: %v", err)
	}

	nfa := exp.NFA()
	srv := &server{exporter: exp, nfaCache: &nfa, words: words}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/nfa", srv.handleNFA)
	mux.HandleFunc("/api/feed", srv.handleFeed)
	mux.HandleFunc("/api/words", srv.handleWords)
	mux.Handle("/", http.FileServer(http.Dir("visualization/static")))

	log.Printf("Listening on %s  (loaded %d words)", *addr, len(words))
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// loadWords reads the word list at path (one word per line) and returns n words
// sampled EVENLY across the whole (alphabetically-sorted) list, so the starting
// letters span the alphabet rather than all being "aa…". With fewer than n
// words available it returns them all.
func loadWords(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var all []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if w := sc.Text(); w != "" {
			all = append(all, w)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if n <= 0 || n >= len(all) {
		return all, nil
	}
	// Stride-sample: index i*len/n spreads the picks uniformly across the list,
	// which (the list being alphabetical) spreads them across starting letters.
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = all[i*len(all)/n]
	}
	return out, nil
}

// handleNFA serves GET /api/nfa.
func (s *server) handleNFA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.nfaCache); err != nil {
		log.Printf("handleNFA encode: %v", err)
	}
}

// handleFeed serves POST /api/feed.
func (s *server) handleFeed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req feedRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	event := []byte(fmt.Sprintf(`{"x": %q}`, req.Word))

	s.mu.Lock()
	feed, err := s.exporter.Feed(event)
	s.mu.Unlock()

	if err != nil {
		http.Error(w, "feed error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(feed); err != nil {
		log.Printf("handleFeed encode: %v", err)
	}
}

// handleWords serves GET /api/words.
func (s *server) handleWords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(s.words); err != nil {
		log.Printf("handleWords encode: %v", err)
	}
}
