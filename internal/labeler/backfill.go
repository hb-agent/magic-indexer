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
	Labels []protoLabel `json:"labels"`
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

// PageHandler receives the decoded labels for a single queryLabels page
// along with the response's next-page cursor. Returning a non-nil error
// aborts the Fetch loop. A handler may persist `nextCursor` to resume
// an interrupted backfill from the same position on the next run.
type PageHandler func(ctx context.Context, labels []protoLabel, nextCursor string) error

// Fetch pages through queryLabels and invokes handle for every batch.
// If sources is empty, the labeler returns labels from all sources it
// knows. Fetch starts from startCursor (empty for a fresh backfill) so
// interrupted runs can resume from the last checkpointed page without
// replaying completed work.
func (b *BackfillClient) Fetch(
	ctx context.Context,
	sources []string,
	startCursor string,
	handle PageHandler,
) error {
	base, err := b.endpoint()
	if err != nil {
		return err
	}

	cursor := startCursor
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

		if err := handle(ctx, parsed.Labels, parsed.Cursor); err != nil {
			return err
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
		// Default to https for bare hosts; normalise ws(s) to http(s).
		switch {
		case strings.HasPrefix(raw, "ws://"):
			raw = "http://" + strings.TrimPrefix(raw, "ws://")
		case strings.HasPrefix(raw, "wss://"):
			raw = "https://" + strings.TrimPrefix(raw, "wss://")
		default:
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
