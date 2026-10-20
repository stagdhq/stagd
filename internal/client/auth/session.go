package auth

import (
	"context"
	"net/http"
	"strings"
)

// SignOut ends the session on the server.
func (c *Client) SignOut(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/auth/sign-out", strings.NewReader("{}"))
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	res, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}

	return res.Body.Close()
}
