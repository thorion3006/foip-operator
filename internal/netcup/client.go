/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package netcup

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	scpBase  = "https://www.servercontrolpanel.de/scp-core"
	tokenURL = "https://www.servercontrolpanel.de/realms/scp/protocol/openid-connect/token"
	clientID = "scp"
)

// Client calls the netcup SCP REST API for failover IP management.
type Client struct {
	userID       int
	refreshToken string
	http         *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

type ErrorClass string

const (
	ErrorClassTransient   ErrorClass = "Transient"
	ErrorClassRateLimited ErrorClass = "RateLimited"
	ErrorClassAuth        ErrorClass = "Authentication"
	ErrorClassValidation  ErrorClass = "Validation"
	ErrorClassTerminal    ErrorClass = "Terminal"
)

// ProviderError preserves safe retry metadata without exposing response data.
type ProviderError struct {
	Class      ErrorClass
	StatusCode int
	RetryAfter time.Duration
	Message    string
}

func (e *ProviderError) Error() string { return e.Message }

func classifyHTTPError(status int) *ProviderError {
	class := ErrorClassTerminal
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		class = ErrorClassAuth
	case status == http.StatusTooManyRequests:
		class = ErrorClassRateLimited
	case status >= 500:
		class = ErrorClassTransient
	case status >= 400:
		class = ErrorClassValidation
	}
	return &ProviderError{Class: class, StatusCode: status, Message: fmt.Sprintf("provider request failed with HTTP %d", status)}
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}

func New(userID int, refreshToken string) *Client {
	return &Client{
		userID:       userID,
		refreshToken: refreshToken,
		http:         &http.Client{},
	}
}

// token returns a valid access token, refreshing it if necessary.
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.tokenExpiry) {
		return c.accessToken, nil
	}
	body := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {c.refreshToken},
		"client_id":     {clientID},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(body.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("refreshing SCP token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading token response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		providerErr := classifyHTTPError(resp.StatusCode)
		providerErr.Message = fmt.Sprintf("SCP token refresh failed with HTTP %d", resp.StatusCode)
		providerErr.RetryAfter = parseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
		return "", providerErr
	}
	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &tr); err != nil {
		return "", fmt.Errorf("parsing token response: %w", err)
	}
	c.accessToken = tr.AccessToken
	c.tokenExpiry = time.Now().Add(time.Duration(tr.ExpiresIn-30) * time.Second)
	return c.accessToken, nil
}

// do executes an authenticated request against the SCP REST API.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader) ([]byte, int, http.Header, error) {
	tok, err := c.token(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, scpBase+path, body)
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("SCP %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header, fmt.Errorf("reading SCP response: %w", err)
	}
	return data, resp.StatusCode, resp.Header, nil
}

type failoverIPv4 struct {
	ID     int `json:"id"`
	Server *struct {
		ID int `json:"id"`
	} `json:"server"`
}

// FindFailoverIP returns the resource ID of the failover IP and the server ID it
// is currently routed to (0 if not yet routed anywhere).
func (c *Client) FindFailoverIP(ctx context.Context, ip string) (foipID int, serverID int, err error) {
	path := fmt.Sprintf("/api/v1/users/%d/failoverips/v4?ip=%s", c.userID, url.QueryEscape(ip))
	data, status, headers, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return 0, 0, err
	}
	if status != http.StatusOK {
		providerErr := classifyHTTPError(status)
		providerErr.RetryAfter = parseRetryAfter(headers.Get("Retry-After"), time.Now())
		return 0, 0, providerErr
	}
	var ips []failoverIPv4
	if err := json.Unmarshal(data, &ips); err != nil {
		return 0, 0, fmt.Errorf("parsing failover IPs: %w", err)
	}
	if len(ips) == 0 {
		return 0, 0, fmt.Errorf("failover IP %s not found in netcup account", ip)
	}
	fi := ips[0]
	if fi.Server != nil {
		serverID = fi.Server.ID
	}
	return fi.ID, serverID, nil
}

// RouteFailoverIP routes foipID to targetServerID and waits for the async task to finish.
func (c *Client) RouteFailoverIP(ctx context.Context, foipID, targetServerID int) error {
	path := fmt.Sprintf("/api/v1/users/%d/failoverips/v4/%d", c.userID, foipID)
	body := fmt.Sprintf(`{"serverId":%d}`, targetServerID)
	data, status, headers, err := c.do(ctx, http.MethodPatch, path, strings.NewReader(body))
	if err != nil {
		return err
	}
	if status != http.StatusAccepted {
		providerErr := classifyHTTPError(status)
		providerErr.RetryAfter = parseRetryAfter(headers.Get("Retry-After"), time.Now())
		return providerErr
	}
	var task struct {
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(data, &task); err != nil {
		return fmt.Errorf("parsing routing task response: %w", err)
	}
	return c.waitForTask(ctx, task.UUID)
}

func (c *Client) waitForTask(ctx context.Context, uuid string) error {
	path := "/api/v1/tasks/" + uuid
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		data, status, _, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("get task HTTP %d: %s", status, data)
		}
		var task struct {
			State   string `json:"state"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(data, &task); err != nil {
			return fmt.Errorf("parsing task: %w", err)
		}
		switch task.State {
		case "FINISHED":
			return nil
		case "ERROR", "CANCELED", "ROLLBACK", "WAITING_FOR_CANCEL":
			if task.Message != "" {
				return fmt.Errorf("routing task %s: %s", task.State, task.Message)
			}
			return fmt.Errorf("routing task ended with state %s", task.State)
		}
		// PENDING or RUNNING → keep polling
	}
}
