// Package porkbuntest provides an in-memory fake of the Porkbun DNS API for tests.
package porkbuntest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"

	pb "github.com/nrdcg/porkbun"
)

// defaultTTL mirrors Porkbun, which stores 600 when no TTL (or a lower one) is sent.
const defaultTTL = 600

// Call is one request the fake received.
type Call struct {
	Action string
	Zone   string
	ID     string
	Record pb.Record
}

// Server is a fake Porkbun API. Record names are stored and returned fully
// qualified, as the real API does, while create/edit take the subdomain only.
type Server struct {
	srv *httptest.Server

	mu      sync.Mutex
	nextID  int
	zones   map[string][]pb.Record
	calls   []Call
	failing map[string]string
}

// New starts a fake serving the given zones and stops it when the test ends.
func New(t testing.TB, zones ...string) *Server {
	t.Helper()
	s := &Server{
		nextID:  1000,
		zones:   map[string][]pb.Record{},
		failing: map[string]string{},
	}
	for _, zone := range zones {
		s.zones[zone] = nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /dns/retrieve/{zone}", s.handle("retrieve"))
	mux.HandleFunc("POST /dns/create/{zone}", s.handle("create"))
	mux.HandleFunc("POST /dns/edit/{zone}/{id}", s.handle("edit"))
	mux.HandleFunc("POST /dns/delete/{zone}/{id}", s.handle("delete"))
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// URL is the API base URL to configure the Porkbun client with.
func (s *Server) URL() *url.URL {
	u, _ := url.Parse(s.srv.URL + "/")
	return u
}

// Seed adds records to a zone as if created through the API, returning their IDs.
func (s *Server) Seed(zone string, recs ...pb.Record) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(recs))
	for _, rec := range recs {
		ids = append(ids, s.create(zone, rec))
	}
	return ids
}

// Records returns a copy of the records stored for a zone.
func (s *Server) Records(zone string) []pb.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.zones[zone])
}

// Calls returns the requests received so far.
func (s *Server) Calls() []Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// ResetCalls forgets the requests received so far.
func (s *Server) ResetCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = nil
}

// Fail makes every following request for action return an API error with message.
func (s *Server) Fail(action string, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing[action] = message
}

func (s *Server) handle(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var rec pb.Record
		if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		zone, id := r.PathValue("zone"), r.PathValue("id")

		s.mu.Lock()
		defer s.mu.Unlock()
		s.calls = append(s.calls, Call{Action: action, Zone: zone, ID: id, Record: rec})

		if message, ok := s.failing[action]; ok {
			writeJSON(w, map[string]string{"status": "ERROR", "message": message})
			return
		}
		if _, ok := s.zones[zone]; !ok {
			writeJSON(w, map[string]string{"status": "ERROR", "message": "Invalid domain."})
			return
		}

		switch action {
		case "retrieve":
			writeJSON(w, map[string]any{"status": "SUCCESS", "records": s.zones[zone]})
		case "create":
			newID, _ := strconv.Atoi(s.create(zone, rec))
			writeJSON(w, map[string]any{"status": "SUCCESS", "id": newID})
		case "edit", "delete":
			i := slices.IndexFunc(s.zones[zone], func(r pb.Record) bool { return r.ID == id })
			if i < 0 {
				writeJSON(w, map[string]string{"status": "ERROR", "message": "Invalid record ID."})
				return
			}
			if action == "edit" {
				rec.ID = id
				s.zones[zone][i] = s.stored(zone, rec)
			} else {
				s.zones[zone] = slices.Delete(s.zones[zone], i, i+1)
			}
			writeJSON(w, map[string]string{"status": "SUCCESS"})
		}
	}
}

func (s *Server) create(zone string, rec pb.Record) string {
	s.nextID++
	rec.ID = strconv.Itoa(s.nextID)
	s.zones[zone] = append(s.zones[zone], s.stored(zone, rec))
	return rec.ID
}

// stored converts a record as sent to the API into the form the API returns.
func (s *Server) stored(zone string, rec pb.Record) pb.Record {
	if rec.Name == "" {
		rec.Name = zone
	} else {
		rec.Name += "." + zone
	}
	if ttl, err := strconv.Atoi(rec.TTL); err != nil || ttl < defaultTTL {
		rec.TTL = strconv.Itoa(defaultTTL)
	}
	if rec.Prio == "" {
		rec.Prio = "0"
	}
	return rec
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
