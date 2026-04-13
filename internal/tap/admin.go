package tap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// AdminClient communicates with the Tap sidecar's admin HTTP API.
type AdminClient struct {
	baseURL  string
	password string
	client   *http.Client
}

// NewAdminClient creates a new Tap admin API client.
func NewAdminClient(baseURL, password string) *AdminClient {
	return &AdminClient{
		baseURL:  baseURL,
		password: password,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Health checks if the Tap sidecar is healthy.
func (c *AdminClient) Health(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/health", nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tap health check failed: %d", resp.StatusCode)
	}
	return nil
}

// AddRepos tells the Tap sidecar to track the given DIDs.
func (c *AdminClient) AddRepos(ctx context.Context, dids []string) error {
	body, err := json.Marshal(map[string]interface{}{"dids": dids})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/repos/add", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth("admin", c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("tap add repos failed: %d", resp.StatusCode)
	}
	return nil
}

// RemoveRepos tells the Tap sidecar to stop tracking the given DIDs.
func (c *AdminClient) RemoveRepos(ctx context.Context, dids []string) error {
	body, err := json.Marshal(map[string]interface{}{"dids": dids})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/repos/remove", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.SetBasicAuth("admin", c.password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("tap remove repos failed: %d", resp.StatusCode)
	}
	return nil
}
