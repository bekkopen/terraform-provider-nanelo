package client_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/madshermansen/terraform-provider-nanelo/internal/client"
	"github.com/madshermansen/terraform-provider-nanelo/internal/fakenanelo"
)

func TestRecordLifecycle(t *testing.T) {
	fake := fakenanelo.New("key", "example.org")
	defer fake.Close()
	c := client.New(fake.BaseURL(), "key", "test")
	ctx := context.Background()

	prio := int64(10)
	for _, r := range []client.Record{
		{Name: "example.org", Type: "MX", Value: "mail.example.org.", TTL: 3600, Priority: &prio},
		{Name: "www.example.org", Type: "A", Value: "192.0.2.1", TTL: 300},
	} {
		if err := c.AddRecord(ctx, "example.org", r); err != nil {
			t.Fatalf("AddRecord(%s): %v", r.Name, err)
		}
	}

	recs := fake.Records("example.org")
	if len(recs) != 2 || recs[0].Name != "example.org" || *recs[0].Priority != 10 || recs[1].TTL != 300 {
		t.Fatalf("unexpected records: %+v", recs)
	}

	// The apex must round-trip through "@"; the bare zone name would address example.org.example.org.
	if err := c.DeleteRecord(ctx, "example.org", "example.org", "MX", "mail.example.org."); err != nil {
		t.Fatalf("DeleteRecord(apex): %v", err)
	}
	if err := c.DeleteRecord(ctx, "example.org", "example.org", "MX", "mail.example.org."); !errors.Is(err, client.ErrNoMatchingRecords) {
		t.Fatalf("second DeleteRecord: got %v, want ErrNoMatchingRecords", err)
	}
	if n := len(fake.Records("example.org")); n != 1 {
		t.Fatalf("got %d records after delete, want 1", n)
	}
}

func TestListZonesAndRecords(t *testing.T) {
	fake := fakenanelo.New("key", "a.example", "b.example")
	defer fake.Close()
	c := client.New(fake.BaseURL(), "key", "test")

	zones, err := c.ListZones(context.Background())
	if err != nil || len(zones) != 2 {
		t.Fatalf("ListZones: %v %v", zones, err)
	}
	recs, err := c.ListRecords(context.Background(), "b.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) == 0 || !recs[0].SystemRecord || recs[0].Priority != nil {
		t.Fatalf("unexpected system records: %+v", recs)
	}
}

func TestAPIError(t *testing.T) {
	fake := fakenanelo.New("key", "example.org")
	defer fake.Close()

	_, err := client.New(fake.BaseURL(), "wrong", "test").ListZones(context.Background())
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 400 || !strings.Contains(apiErr.Message, "Invalid API Key") {
		t.Fatalf("got %v, want Invalid API Key APIError", err)
	}
}

func TestKeyIsSentInHeaderOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/header/dns/getzones" || r.Header.Get("X-API-KEY") != "secret" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"ok":true,"result":{"zones":[]}}`))
	}))
	defer srv.Close()
	if _, err := client.New(srv.URL+"/v1", "secret", "test").ListZones(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// The API never redirects. Following one would send the key, which Go forwards in custom
// headers even across hosts, to wherever the redirect points.
func TestRedirectsAreNotFollowed(t *testing.T) {
	var leaked atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "" {
			leaked.Store(true)
		}
		w.Write([]byte(`{"ok":true,"result":{"zones":[]}}`))
	}))
	defer elsewhere.Close()
	srv := httptest.NewServer(http.RedirectHandler(elsewhere.URL, http.StatusTemporaryRedirect))
	defer srv.Close()

	_, err := client.New(srv.URL+"/v1", "secret", "test").ListZones(context.Background())
	if leaked.Load() {
		t.Fatal("API key was sent to the redirect target")
	}
	if err == nil {
		t.Fatal("expected an error for a redirect response")
	}
}

func TestRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("<html>bad gateway</html>"))
			return
		}
		w.Write([]byte(`{"ok":true,"result":{"zones":["example.org"]}}`))
	}))
	defer srv.Close()
	c := client.New(srv.URL+"/v1", "key", "test")

	if _, err := c.ListZones(context.Background()); err != nil || calls.Load() != 3 {
		t.Fatalf("ListZones: err=%v calls=%d, want success after 3 calls", err, calls.Load())
	}

	calls.Store(0)
	err := c.AddRecord(context.Background(), "example.org", client.Record{Name: "a.example.org", Type: "A", Value: "192.0.2.1"})
	if err == nil || calls.Load() != 1 {
		t.Fatalf("AddRecord: err=%v calls=%d, want a single failed call", err, calls.Load())
	}
}
