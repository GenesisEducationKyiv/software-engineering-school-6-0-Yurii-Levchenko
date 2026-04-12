package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Client handles all communication with GitHub API
type Client struct {
	token      string
	httpClient *http.Client
}

// create a new GitHub API client
func New(token string) *Client {
	return &Client{
		token: token,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// releaseResponse maps the JSON response from GitHub's releases API
type releaseResponse struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
}

// CheckRepoExists verifies that a GitHub repository exists
// Returns true if found, false if 404, error on other failures
func (c *Client) CheckRepoExists(owner, repo string) (bool, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, err
	}
	c.setHeaders(req)

	resp, err := c.doWithRetry(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return true, nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	return false, fmt.Errorf("github API returned status %d", resp.StatusCode)
}

// GetLatestRelease returns the latest release tag for a repo
// Returns empty string if no releases exist
func (c *Client) GetLatestRelease(owner, repo string) (string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", err
	}
	c.setHeaders(req)

	resp, err := c.doWithRetry(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return "", nil // no releases yet
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github API returned status %d", resp.StatusCode)
	}

	var release releaseResponse
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}

	return release.TagName, nil
}

// setHeaders adds common headers to GitHub API requests
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github.v3+json")
	req.Header.Set("User-Agent", "github-release-notifier")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// doWithRetry executes a request with retry on 429 (rate limit)
// GitHub rate limit is 60 req/hr without token but 
// I attached token so it should be 5000req/hr
func (c *Client) doWithRetry(req *http.Request) (*http.Response, error) {
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		// if not rate limited, return immediately
		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}

		resp.Body.Close()

		// exponential backoff: 2s, 4s, 8s
		backoff := time.Duration(1<<uint(i+1)) * time.Second
		time.Sleep(backoff)
	}

	// final attempt
	return c.httpClient.Do(req)
}
