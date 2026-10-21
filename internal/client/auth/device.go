// Package auth signs the CLI in with device login (RFC 8628) against the
// Stagd API's sign-in endpoints, and stores the resulting session token.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// ClientID is the CLI's device-login client id; the API refuses any other.
const ClientID = "stagd-cli"

const grantType = "urn:ietf:params:oauth:grant-type:device_code"

// DeviceCode is what the API returns when a device login starts.
type DeviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// Errors the person can act on.
var (
	ErrDenied  = errors.New("sign-in was denied in the browser")
	ErrExpired = errors.New("the code expired before it was approved; run `stagd login` again")
)

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Client talks to the API's /auth endpoints.
type Client struct {
	BaseURL string
	HTTP    *http.Client

	// Version is the CLI's, shown on the page where the person approves the sign-in.
	Version string
}

func (c *Client) post(ctx context.Context, path string, body any, out any) (int, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Type", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return res.StatusCode, fmt.Errorf("unexpected response from %s (%d)", path, res.StatusCode)
	}

	return res.StatusCode, nil
}

// Start begins a device login.
func (c *Client) Start(ctx context.Context) (*DeviceCode, error) {
	var res struct {
		DeviceCode
		tokenResponse
	}

	// What the person approving sees, so they can tell it's their terminal.
	host, _ := os.Hostname()
	body := map[string]string{
		"client_id":       ClientID,
		"client_version":  c.Version,
		"client_platform": runtime.GOOS + "/" + runtime.GOARCH,
		"device_name":     host,
	}

	status, err := c.post(ctx, "/auth/device/code", body, &res)
	if err != nil {
		return nil, err
	}

	if status != http.StatusOK || res.DeviceCode.DeviceCode == "" {
		return nil, fmt.Errorf("could not start sign-in (%d %s)", status, res.Error)
	}

	return &res.DeviceCode, nil
}

// Wait polls until the person approves in the browser, then returns the session token.
func (c *Client) Wait(ctx context.Context, code *DeviceCode) (string, error) {
	interval := time.Duration(max(code.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(code.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}

		var res tokenResponse
		body := map[string]string{"grant_type": grantType, "device_code": code.DeviceCode, "client_id": ClientID}

		if _, err := c.post(ctx, "/auth/device/token", body, &res); err != nil {
			// A dropped connection while waiting isn't fatal: try again until the code expires.
			if ctx.Err() != nil {
				return "", ctx.Err()
			}

			continue
		}

		if res.AccessToken != "" {
			return res.AccessToken, nil
		}

		switch res.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "access_denied":
			return "", ErrDenied
		case "expired_token":
			return "", ErrExpired
		default:
			return "", fmt.Errorf("sign-in failed: %s %s", res.Error, res.ErrorDescription)
		}
	}

	return "", ErrExpired
}
