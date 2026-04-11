package labeler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
)

// MaxBackfillBodyBytes bounds the size of a single queryLabels HTTP response
// so a malicious or misbehaving labeler can't exhaust memory.
const MaxBackfillBodyBytes = 10 << 20 // 10 MiB

// queryLabelsResponse is the shape returned by com.atproto.label.queryLabels.
type queryLabelsResponse struct {
	Cursor string       `json:"cursor,omitempty"`
	Labels []ProtoLabel `json:"labels"`
}

// BackfillClient pages through com.atproto.label.queryLabels to fetch
// existing labels before we connect the live subscription.
type BackfillClient struct {
	http    *retryablehttp.Client
	pdsHost string
}

// NewBackfillClient creates a backfill client for the given PDS host.
// Redirects are explicitly rejected so that a malicious or compromised
// labeler endpoint cannot 301 our requests to an attacker-controlled
// host and exfiltrate headers or response bodies. queryLabels is
// authoritative at its PDS URL and should never legitimately redirect.
func NewBackfillClient(pdsHost string) *BackfillClient {
	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil
	httpClient.RetryMax = 3
	httpClient.HTTPClient.Timeout = 30 * time.Second
	httpClient.HTTPClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &BackfillClient{
		http:    httpClient,
		pdsHost: pdsHost,
	}
}

// MaxBackfillPages bounds the number of queryLabels pages we'll fetch in a
// single Fetch call. At limit=250 per page this allows ~10M labels, which
// far exceeds any realistic labeler backfill and prevents a runaway loop
// if the server returns an unexpected cursor sequence.
const MaxBackfillPages = 40_000

// Fetch pages through queryLabels and invokes handle for every batch.
// If sources is empty, the labeler returns labels from all sources it knows.
// handle is called once per page; it may return an error to abort.
func (b *BackfillClient) Fetch(
	ctx context.Context,
	sources []string,
	handle func(ctx context.Context, labels []ProtoLabel) error,
) error {
	base, err := b.endpoint()
	if err != nil {
		return err
	}

	var cursor string
	pages := 0
	for {
		if pages >= MaxBackfillPages {
			return fmt.Errorf("queryLabels exceeded %d pages without termination", MaxBackfillPages)
		}
		pages++
		q := url.Values{}
		q.Set("uriPatterns", "*")
		q.Set("limit", "250")
		for _, src := range sources {
			q.Add("sources", src)
		}
		if cursor != "" {
			q.Set("cursor", cursor)
		}

		reqURL := base + "?" + q.Encode()
		req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
		if err != nil {
			return fmt.Errorf("build queryLabels request: %w", err)
		}
		req.Header.Set("Accept", "application/json")

		resp, err := b.http.Do(req)
		if err != nil {
			return fmt.Errorf("queryLabels request: %w", err)
		}

		body, err := io.ReadAll(io.LimitReader(resp.Body, MaxBackfillBodyBytes+1))
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read queryLabels response: %w", err)
		}
		if int64(len(body)) > MaxBackfillBodyBytes {
			return fmt.Errorf("queryLabels response exceeded %d bytes", MaxBackfillBodyBytes)
		}

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("queryLabels returned %d: %s", resp.StatusCode, string(body))
		}

		var parsed queryLabelsResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			return fmt.Errorf("parse queryLabels response: %w", err)
		}

		if len(parsed.Labels) > 0 {
			if err := handle(ctx, parsed.Labels); err != nil {
				return err
			}
		}

		if parsed.Cursor == "" || parsed.Cursor == cursor {
			return nil
		}
		cursor = parsed.Cursor
	}
}

func (b *BackfillClient) endpoint() (string, error) {
	raw := b.pdsHost
	if raw == "" {
		return "", fmt.Errorf("PDSHost is empty")
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		// Default to https for bare hosts.
		if strings.HasPrefix(raw, "ws://") {
			raw = "http://" + strings.TrimPrefix(raw, "ws://")
		} else if strings.HasPrefix(raw, "wss://") {
			raw = "https://" + strings.TrimPrefix(raw, "wss://")
		} else {
			raw = "https://" + raw
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	u.Path = "/xrpc/com.atproto.label.queryLabels"
	u.RawQuery = ""
	return u.String(), nil
}
