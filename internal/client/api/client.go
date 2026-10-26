// Package api builds Connect clients for the Stagd API.
package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"

	"github.com/stagdhq/stagd/internal/gen/stagd/api/v1/apiv1connect"
)

// Config says where the API is and who is calling.
type Config struct {
	BaseURL   string
	Token     string
	UserAgent string
}

// Clients holds one client per API service.
type Clients struct {
	Auth     apiv1connect.AuthServiceClient
	Orgs     apiv1connect.OrgServiceClient
	Scm      apiv1connect.ScmServiceClient
	Compute  apiv1connect.ComputeServiceClient
	Previews apiv1connect.PreviewServiceClient
	Secrets  apiv1connect.SecretServiceClient
	Activity apiv1connect.ActivityServiceClient

	Integrations apiv1connect.IntegrationServiceClient
}

// ErrNoToken means the command needs a signed-in user.
var ErrNoToken = errors.New("not signed in: run `stagd login`")

// New returns clients for every service. It fails without a token: every
// API call runs as the signed-in person.
func New(cfg Config) (*Clients, error) {
	if cfg.Token == "" {
		return nil, ErrNoToken
	}

	httpClient := &http.Client{Timeout: 30 * time.Second}
	// Previews stream (WatchPreview) for as long as a build takes, so no
	// overall timeout; connecting and the first response still time out.
	streaming := &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}}
	opts := []connect.ClientOption{connect.WithInterceptors(auth(cfg))}

	return &Clients{
		Auth:     apiv1connect.NewAuthServiceClient(httpClient, cfg.BaseURL, opts...),
		Orgs:     apiv1connect.NewOrgServiceClient(httpClient, cfg.BaseURL, opts...),
		Scm:      apiv1connect.NewScmServiceClient(httpClient, cfg.BaseURL, opts...),
		Compute:  apiv1connect.NewComputeServiceClient(httpClient, cfg.BaseURL, opts...),
		Previews: apiv1connect.NewPreviewServiceClient(streaming, cfg.BaseURL, opts...),
		Secrets:  apiv1connect.NewSecretServiceClient(httpClient, cfg.BaseURL, opts...),
		Activity: apiv1connect.NewActivityServiceClient(httpClient, cfg.BaseURL, opts...),

		Integrations: apiv1connect.NewIntegrationServiceClient(httpClient, cfg.BaseURL, opts...),
	}, nil
}

// auth adds the bearer token and user agent to every request.
func auth(cfg Config) connect.Interceptor {
	return headerInterceptor(func(h http.Header) {
		h.Set("Authorization", "Bearer "+cfg.Token)
		if cfg.UserAgent != "" {
			h.Set("User-Agent", cfg.UserAgent)
		}
	})
}

type headerInterceptor func(http.Header)

func (f headerInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		f(req.Header())

		return next(ctx, req)
	}
}

func (f headerInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		f(conn.RequestHeader())

		return conn
	}
}

func (f headerInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
