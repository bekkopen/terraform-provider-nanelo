// Package client is a minimal client for the Nanelo DNS API (https://nanelo.com/docs).
//
// Behaviour of the API that is not in the official docs, verified against the live API:
//   - Records have no ID. deleterecord removes every record matching (name, type, value),
//     regardless of TTL or priority. Exact duplicates can be created.
//   - Names ending in ".<zone>" are treated as FQDNs; anything else, including the bare zone
//     name, gets ".<zone>" appended. The apex must be addressed as "@".
//   - Names are stored lowercased with any trailing dot stripped. Values are stored verbatim
//     after trimming surrounding whitespace, and deleterecord compares them case-insensitively.
//   - TTLs are rounded to the nearest of SupportedTTLs; 0 or a missing TTL means 3600.
//   - getrecords reports a priority of 0 as null.
//   - Errors are returned as HTTP 400 with {"ok": false, "error": "..."}.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const DefaultBaseURL = "https://api.nanelo.com/v1"

// SupportedTTLs are the only TTLs Nanelo stores; other values are silently rounded.
var SupportedTTLs = []int64{60, 300, 900, 1800, 3600, 7200, 18000, 43200, 86400}

const DefaultTTL = 3600

// SupportedTypes are the record types accepted by addrecord.
var SupportedTypes = []string{"A", "AAAA", "ALIAS", "CAA", "CNAME", "MX", "NS", "PTR", "SRV", "TLSA", "TXT"}

// ErrNoMatchingRecords is returned by DeleteRecord when nothing matched.
var ErrNoMatchingRecords = errors.New("no matching records found")

type Record struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Value        string `json:"value"`
	TTL          int64  `json:"ttl"`
	Priority     *int64 `json:"priority"`
	SystemRecord bool   `json:"system_record"`
}

// APIError is an error reported by the API through {"ok": false}.
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("nanelo API error (HTTP %d): %s", e.StatusCode, e.Message)
}

type Client struct {
	baseURL    string
	apiKey     string
	userAgent  string
	httpClient *http.Client
	retryWait  time.Duration
}

func New(baseURL, apiKey, userAgent string) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		apiKey:     apiKey,
		userAgent:  userAgent,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		retryWait:  time.Second,
	}
}

func (c *Client) ListZones(ctx context.Context) ([]string, error) {
	var result struct {
		Zones []string `json:"zones"`
	}
	if err := c.do(ctx, "dns/getzones", nil, true, &result); err != nil {
		return nil, err
	}
	return result.Zones, nil
}

func (c *Client) ListRecords(ctx context.Context, zone string) ([]Record, error) {
	var result struct {
		Records []Record `json:"records"`
	}
	if err := c.do(ctx, "dns/getrecords", url.Values{"domain": {zone}}, true, &result); err != nil {
		return nil, err
	}
	return result.Records, nil
}

// AddRecord creates a record. name must be the record's FQDN within zone. It is not retried:
// the API is not idempotent, so a retry after an ambiguous failure could create a duplicate.
func (c *Client) AddRecord(ctx context.Context, zone string, r Record) error {
	params := url.Values{
		"domain": {zone},
		"name":   {apiName(zone, r.Name)},
		"type":   {r.Type},
		"value":  {r.Value},
		"ttl":    {strconv.FormatInt(r.TTL, 10)},
	}
	if r.Priority != nil {
		params.Set("priority", strconv.FormatInt(*r.Priority, 10))
	}
	return c.do(ctx, "dns/addrecord", params, false, nil)
}

// DeleteRecord deletes every record in zone matching name, type and value. name must be the
// record's FQDN within zone. Returns ErrNoMatchingRecords if nothing matched.
func (c *Client) DeleteRecord(ctx context.Context, zone, name, typ, value string) error {
	params := url.Values{
		"domain": {zone},
		"name":   {apiName(zone, name)},
		"type":   {typ},
		"value":  {value},
	}
	err := c.do(ctx, "dns/deleterecord", params, true, nil)
	var apiErr *APIError
	if errors.As(err, &apiErr) && strings.Contains(apiErr.Message, "No matching Records found") {
		return ErrNoMatchingRecords
	}
	return err
}

// apiName converts an FQDN to the form the API expects: the bare zone name would be
// interpreted as "<zone>.<zone>", so the apex has to be sent as "@".
func apiName(zone, name string) string {
	if strings.EqualFold(strings.TrimSuffix(name, "."), zone) {
		return "@"
	}
	return name
}

type envelope struct {
	OK     bool            `json:"ok"`
	Error  string          `json:"error"`
	Result json.RawMessage `json:"result"`
}

// do POSTs params as a form body and decodes the result into out. The key is always sent in
// the X-API-KEY header: a key in the URL path would end up in error messages and debug logs.
func (c *Client) do(ctx context.Context, endpoint string, params url.Values, retry bool, out any) error {
	attempts := 1
	if retry {
		attempts = 3
	}
	var err error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(c.retryWait * time.Duration(i)):
			}
		}
		var retryable bool
		retryable, err = c.doOnce(ctx, endpoint, params, out)
		if err == nil || !retryable {
			return err
		}
	}
	return err
}

func (c *Client) doOnce(ctx context.Context, endpoint string, params url.Values, out any) (retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/header/"+endpoint, strings.NewReader(params.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("X-API-KEY", c.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ctx.Err() == nil, fmt.Errorf("calling %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return true, fmt.Errorf("reading %s response: %w", endpoint, err)
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return resp.StatusCode >= 500, fmt.Errorf("calling %s: unexpected HTTP %d response: %.200s", endpoint, resp.StatusCode, body)
	}
	if !env.OK {
		msg := env.Error
		if msg == "" {
			msg = "unknown error"
		}
		return resp.StatusCode >= 500, &APIError{StatusCode: resp.StatusCode, Message: msg}
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return false, fmt.Errorf("decoding %s result: %w", endpoint, err)
		}
	}
	return false, nil
}
