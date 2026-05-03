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
	"devup/internal/version"
	"devup/internal/workspace"
)

const (
	BaseURL        = "http://127.0.0.1:7777"
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

// NewWithAddr creates a client targeting a specific agent by address and port.
func NewWithAddr(addr string, port int, token string) *Client {
	return &Client{
		baseURL: fmt.Sprintf("http://%s:%d", addr, port),
		token:   token,
		http: &http.Client{
			Timeout: ConnectTimeout,
			Transport: &http.Transport{
				ResponseHeaderTimeout: ConnectTimeout,
			},
		},
	}
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("X-Devup-Token", c.token)
	req.Header.Set("X-Devup-Version", version.Version)
}

// Health checks agent health
func (c *Client) Health(ctx context.Context) (*api.HealthResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
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
	c.setHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	// Toolchain hydration and shadow workspace prep can delay first bytes.
	// Disable client-side header and body deadlines; the caller's context is the bound.
	transport := c.http.Transport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 0
	streamClient := &http.Client{
		Transport: transport,
		Timeout:   0,
	}
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
	c.setHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	transport := c.http.Transport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 0
	startClient := &http.Client{
		Transport: transport,
		Timeout:   0,
	}
	resp, err := startClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return "", httpErrorWithBody("start", resp)
	}
	if resp.StatusCode != http.StatusOK {
		return "", httpErrorWithBody("start", resp)
	}
	var sr api.StartResponse
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return "", err
	}
	return sr.JobID, nil
}

func httpErrorWithBody(prefix string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return fmt.Errorf("%s: %s", prefix, resp.Status)
	}
	return fmt.Errorf("%s: %s: %s", prefix, resp.Status, msg)
}

// Ps lists all jobs
func (c *Client) Ps(ctx context.Context) ([]api.JobInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/ps", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
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
	c.setHeaders(req)
	httpClient := c.http
	if follow {
		// No timeout, no ResponseHeaderTimeout - agent flushes headers immediately
		// but we must not timeout while waiting for streaming body
		transport := c.http.Transport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 0
		httpClient = &http.Client{
			Transport: transport,
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
	c.setHeaders(req)
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

// SystemInfo fetches tool version info from the agent in a single round-trip
func (c *Client) SystemInfo(ctx context.Context) (*api.SystemInfoResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/system/info", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("system/info: %s", resp.Status)
	}
	var si api.SystemInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&si); err != nil {
		return nil, err
	}
	return &si, nil
}

// Cluster fetches discovered cluster peers from the agent
func (c *Client) Cluster(ctx context.Context) ([]api.PeerInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/cluster", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("agent rejected token (401); run 'devup vm reset-token' and restart VM")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cluster: %s", resp.Status)
	}
	var cr api.ClusterResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, err
	}
	return cr.Peers, nil
}

// Upload streams a tar archive of dir to the agent's /upload endpoint,
// returning the remote workspace path. Used for cluster scheduling to ship
// code to a remote node.
func (c *Client) Upload(ctx context.Context, dir string) (string, error) {
	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- workspace.StreamTar(dir, workspace.DefaultExcludes, pw)
		pw.Close()
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/upload", pr)
	if err != nil {
		return "", err
	}
	c.setHeaders(req)
	req.Header.Set("Content-Type", "application/x-tar")

	uploadClient := &http.Client{Transport: c.http.Transport, Timeout: 0}
	resp, err := uploadClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	if tarErr := <-errCh; tarErr != nil {
		return "", fmt.Errorf("tar: %w", tarErr)
	}

	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("agent rejected token (401)")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("upload: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var ur api.UploadResponse
	if err := json.NewDecoder(resp.Body).Decode(&ur); err != nil {
		return "", err
	}
	return ur.WorkspacePath, nil
}

// Down stops all running jobs; returns count stopped
func (c *Client) Down(ctx context.Context) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/down", nil)
	if err != nil {
		return 0, err
	}
	c.setHeaders(req)
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
