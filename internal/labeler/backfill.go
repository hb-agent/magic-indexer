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
func NewBackfillClient(pdsHost string) *BackfillClient {
	httpClient := retryablehttp.NewClient()
	httpClient.Logger = nil
	httpClient.RetryMax = 3
	httpClient.HTTPClient.Timeout = 30 * time.Second
	return &BackfillClient{
		http:    httpClient,
		pdsHost: pdsHost,
	}
}

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
	for {
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

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("read queryLabels response: %w", err)
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
