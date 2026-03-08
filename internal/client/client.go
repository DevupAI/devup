package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"devup/internal/api"
)

const (
	BaseURL       = "http://127.0.0.1:7777"
	ConnectTimeout = 5 * time.Second
	ExitCodePrefix = "DEVUP_EXIT_CODE="
)

// Client talks to the devup-agent
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New creates a client with token auth
func New(token string) *Client {
	return &Client{
		baseURL: BaseURL,
		token:   token,
		http: &http.Client{
			Timeout: ConnectTimeout,
			Transport: &http.Transport{
				ResponseHeaderTimeout: ConnectTimeout,
			},
		},
	}
}

// Health checks agent health
func (c *Client) Health(ctx context.Context) (*api.HealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Devup-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("health check: %s", resp.Status)
	}
	var h api.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, err
	}
	return &h, nil
}

// Run executes a command and streams output; returns exit code
// No total timeout for streaming - runs until done or context cancelled
func (c *Client) Run(ctx context.Context, req *api.RunRequest, out io.Writer) (int, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return -1, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/run", bytes.NewReader(body))
	if err != nil {
		return -1, err
	}
	httpReq.Header.Set("X-Devup-Token", c.token)
	httpReq.Header.Set("Content-Type", "application/json")
	// No total timeout for streaming
	streamClient := &http.Client{Transport: c.http.Transport}
	streamClient.Timeout = 0
	resp, err := streamClient.Do(httpReq)
	if err != nil {
		return -1, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return -1, fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode != http.StatusOK {
		return -1, fmt.Errorf("run: %s", resp.Status)
	}
	// Parse plain text stream; last line may be DEVUP_EXIT_CODE=<n>
	scanner := bufio.NewScanner(resp.Body)
	var lastLine string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, ExitCodePrefix) {
			lastLine = line
			continue
		}
		if out != nil {
			fmt.Fprintln(out, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return -1, err
	}
	if lastLine != "" {
		var code int
		if _, err := fmt.Sscanf(lastLine, ExitCodePrefix+"%d", &code); err == nil {
			return code, nil
		}
	}
	return 0, nil
}

// Start starts a background job; returns job ID
func (c *Client) Start(ctx context.Context, req *api.StartRequest) (string, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/start", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("X-Devup-Token", c.token)
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", fmt.Errorf("too many concurrent jobs")
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("start: %s", resp.Status)
	}
	var sr api.StartResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", err
	}
	return sr.JobID, nil
}

// Ps lists all jobs
func (c *Client) Ps(ctx context.Context) ([]api.JobInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/ps", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Devup-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ps: %s", resp.Status)
	}
	var pr api.PsResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, err
	}
	return pr.Jobs, nil
}

// Logs streams job logs to out; if follow is true, streams until job ends or context cancelled
func (c *Client) Logs(ctx context.Context, jobID string, follow bool, out io.Writer) error {
	url := c.baseURL + "/logs?id=" + jobID
	if follow {
		url += "&follow=1"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Devup-Token", c.token)
	httpClient := c.http
	if follow {
		httpClient = &http.Client{
			Transport: c.http.Transport,
			Timeout:   0,
		}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("job not found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("logs: %s", resp.Status)
	}
	if out != nil {
		_, err = io.Copy(out, resp.Body)
		return err
	}
	return nil
}

// Stop stops a job
func (c *Client) Stop(ctx context.Context, jobID string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/stop?id="+jobID, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Devup-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("job not found")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stop: %s", resp.Status)
	}
	return nil
}

// Down stops all running jobs; returns count stopped
func (c *Client) Down(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/down", nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("X-Devup-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return 0, fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("down: %s", resp.Status)
	}
	var result struct {
		Stopped int `json:"stopped"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return 0, nil
	}
	return result.Stopped, nil
}
