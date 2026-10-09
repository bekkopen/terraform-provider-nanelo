// Package fakenanelo is an in-memory stand-in for the Nanelo DNS API, used in tests.
//
// It reproduces the behaviour observed on the live API (see the client package doc), including
// the awkward parts: duplicate records, tuple-wide deletes, TTL rounding, name rewriting and
// the "@" apex. Error messages are copied verbatim from the real API.
package fakenanelo

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/bekk/terraform-provider-nanelo/internal/client"
)

type Server struct {
	*httptest.Server
	APIKey string

	mu      sync.Mutex
	zones   []string
	records map[string][]client.Record
}

// New starts a fake API. With one zone the key behaves like a domain-bound key; with several
// it behaves like a team key, where every call except getzones needs a domain parameter.
func New(apiKey string, zones ...string) *Server {
	s := &Server{APIKey: apiKey, zones: zones, records: map[string][]client.Record{}}
	for _, z := range zones {
		s.records[z] = []client.Record{
			{Name: z, Type: "NS", Value: "ns1.nanelo.com", TTL: 43200, SystemRecord: true},
			{Name: z, Type: "NS", Value: "ns2.nanelo.com", TTL: 43200, SystemRecord: true},
			{Name: z, Type: "SOA", Value: "ns1.nanelo.com hostmaster.nanelo.com 2026100900 43200 7200 1209600 3600", TTL: 3600, SystemRecord: true},
		}
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

// BaseURL is the value to pass to client.New.
func (s *Server) BaseURL() string { return s.URL + "/v1" }

// Records returns the non-system records of zone.
func (s *Server) Records(zone string) []client.Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []client.Record
	for _, r := range s.records[zone] {
		if !r.SystemRecord {
			out = append(out, r)
		}
	}
	return out
}

// Put inserts a record directly, as if it had been created outside Terraform.
func (s *Server) Put(zone string, r client.Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[zone] = append(s.records[zone], r)
}

// Remove deletes matching records directly, as if they had been deleted outside Terraform.
func (s *Server) Remove(zone string, match func(client.Record) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records[zone] = slices.DeleteFunc(s.records[zone], match)
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		fail(w, "Missing parameters.")
		return
	}
	if r.Header.Get("X-API-KEY") != s.APIKey || !strings.HasPrefix(r.URL.Path, "/v1/header/") {
		fail(w, "Invalid API Key. You can find the API documentation at nanelo.com/docs")
		return
	}
	endpoint := strings.TrimPrefix(r.URL.Path, "/v1/header/")
	if endpoint == "dns/getzones" {
		ok(w, map[string]any{"zones": s.zones})
		return
	}

	zone, msg := s.zone(r.Form.Get("domain"))
	if msg != "" {
		fail(w, msg)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	switch endpoint {
	case "dns/getrecords":
		recs := make([]client.Record, 0, len(s.records[zone]))
		recs = append(recs, s.records[zone]...)
		ok(w, map[string]any{"records": recs})
	case "dns/addrecord":
		s.add(w, r, zone)
	case "dns/deleterecord":
		s.delete(w, r, zone)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Server) zone(domain string) (string, string) {
	if len(s.zones) == 1 {
		if domain != "" && domain != s.zones[0] {
			return "", "Invalid Domain set. This is a Domain-specific API Key, but you set a different domain as a parameter. Either correct the domain parameter or remove it. You can find the API documentation at nanelo.com/docs"
		}
		return s.zones[0], ""
	}
	if !slices.Contains(s.zones, domain) {
		return "", "Invalid Domain"
	}
	return domain, ""
}

func (s *Server) add(w http.ResponseWriter, r *http.Request, zone string) {
	name, typ, value := r.Form.Get("name"), strings.ToUpper(r.Form.Get("type")), strings.TrimSpace(r.Form.Get("value"))
	if name == "" || typ == "" || value == "" {
		fail(w, "Missing parameters.")
		return
	}
	if !slices.Contains(client.SupportedTypes, typ) {
		fail(w, "Couldn't save DNS Record. Unsupported Record Type.")
		return
	}
	if ip := net.ParseIP(value); (typ == "A" && (ip == nil || ip.To4() == nil)) || (typ == "AAAA" && (ip == nil || ip.To4() != nil)) {
		fail(w, "Couldn't save DNS Record. Inavlid Value. Value is not supported by "+typ+" Records.")
		return
	}
	if strings.ContainsFunc(value, func(c rune) bool { return c > 0xFFFF }) {
		fail(w, "Couldn't save DNS Record. Unknown error - Record couldn't be saved.")
		return
	}

	rec := client.Record{Name: normalizeName(zone, name), Type: typ, Value: value, TTL: roundTTL(r.Form.Get("ttl"))}
	if p, _ := strconv.ParseInt(r.Form.Get("priority"), 10, 64); p != 0 {
		rec.Priority = &p
	}
	s.records[zone] = append(s.records[zone], rec)
	ok(w, map[string]any{"text": "DNS Record added successfully.", "record": rec})
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request, zone string) {
	name, typ, value := r.Form.Get("name"), r.Form.Get("type"), r.Form.Get("value")
	if name == "" || typ == "" || value == "" {
		fail(w, "Missing parameters.")
		return
	}
	name = normalizeName(zone, name)
	before := len(s.records[zone])
	// Whether the real API lets system records be deleted is unverified; the fake refuses.
	s.records[zone] = slices.DeleteFunc(s.records[zone], func(rec client.Record) bool {
		return !rec.SystemRecord && rec.Name == name && strings.EqualFold(rec.Type, typ) && strings.EqualFold(rec.Value, value)
	})
	n := before - len(s.records[zone])
	switch n {
	case 0:
		fail(w, "Couldn't delete DNS Record. No matching Records found.")
	case 1:
		ok(w, map[string]any{"text": "1 matching DNS Record deleted successfully."})
	default:
		ok(w, map[string]any{"text": strconv.Itoa(n) + " matching DNS Records deleted successfully."})
	}
}

func normalizeName(zone, name string) string {
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if name == "@" {
		return zone
	}
	if !strings.HasSuffix(name, "."+zone) {
		return name + "." + zone // also turns the bare zone name into "<zone>.<zone>"
	}
	return name
}

// roundTTL picks the nearest supported TTL, preferring the lower one on ties.
func roundTTL(raw string) int64 {
	f, _ := strconv.ParseFloat(raw, 64)
	if f <= 0 {
		return client.DefaultTTL
	}
	best := client.SupportedTTLs[0]
	for _, t := range client.SupportedTTLs {
		if math.Abs(float64(t)-f) < math.Abs(float64(best)-f) {
			best = t
		}
	}
	return best
}

func ok(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func fail(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": msg})
}
