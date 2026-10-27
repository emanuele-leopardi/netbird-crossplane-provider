/*
Copyright 2022 The Crossplane Authors.

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

// Package nb contains the shared NetBird plumbing used by every managed
// resource controller.
package nb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	netbird "github.com/netbirdio/netbird/management/client/rest"

	"github.com/emanuele-leopardi/netbird-crossplane-provider/internal/clients"
)

const (
	errGetCreds = "cannot get credentials"
)

// AuthManager handles authentication and token refresh for the NetBird API.
type AuthManager struct {
	mu            sync.Mutex
	client        *netbird.Client
	oauthConfig   string
	issuerURL     string
	credType      string
	endpoint      string
	lastTokenTime time.Time
	expiresIn     time.Duration
	log           logr.Logger
}

// TokenResponse represents the OAuth token response.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"` // Lifetime in seconds
}

// NewAuthManager creates a new authentication manager.
func NewAuthManager(endpoint, creds, credType, issuerURL string) *AuthManager {
	return &AuthManager{
		oauthConfig: creds,
		issuerURL:   issuerURL,
		credType:    credType,
		endpoint:    endpoint,
		log:         ctrl.Log.WithName("auth-manager"),
	}
}

// GetClient returns a valid authenticated client, refreshing the token if
// needed.
func (a *AuthManager) GetClient(ctx context.Context) (*netbird.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.client == nil || a.tokenNeedsRefresh() {
		a.log.V(1).Info("Refreshing NetBird API token")
		if err := a.refreshToken(ctx); err != nil {
			return nil, errors.Wrap(err, "failed to refresh token")
		}
	}
	return a.client, nil
}

// IsTokenInvalidError checks if the error indicates an invalid token.
func IsTokenInvalidError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "token invalid") ||
		strings.Contains(errStr, "unauthorized") ||
		strings.Contains(errStr, "401")
}

// ForceRefresh forces a token refresh regardless of the current token status.
func (a *AuthManager) ForceRefresh(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.log.V(1).Info("Force refreshing NetBird API token")
	return a.refreshToken(ctx)
}

func (a *AuthManager) tokenNeedsRefresh() bool {
	// Refresh if the token is expired or will expire within five minutes.
	return !a.lastTokenTime.IsZero() && time.Since(a.lastTokenTime) > (a.expiresIn-5*time.Minute)
}

func (a *AuthManager) refreshToken(ctx context.Context) error {
	var token string
	var expiresIn time.Duration
	var err error

	switch a.credType {
	case "oauth":
		token, expiresIn, err = a.getOauthToken(ctx)
		if err != nil {
			return errors.Wrap(err, "failed to get OAuth token")
		}
	default:
		// For non-OAuth credentials (like a personal access token or a JWT),
		// use the value directly.
		token = a.oauthConfig
		expiresIn = 24 * time.Hour
	}

	a.client = netbird.NewWithBearerToken(a.endpoint, token)
	a.lastTokenTime = time.Now()
	a.expiresIn = expiresIn
	a.log.V(1).Info("Token refreshed")
	return nil
}

func (a *AuthManager) getOauthToken(ctx context.Context) (string, time.Duration, error) {
	var tokenRequest struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		GrantType    string `json:"grant_type"`
		Scope        string `json:"scope"`
	}

	if err := json.Unmarshal([]byte(a.oauthConfig), &tokenRequest); err != nil {
		return "", 0, errors.Wrap(err, "failed to unmarshal OAuth config")
	}

	formBody := url.Values{}
	formBody.Set("client_id", tokenRequest.ClientID)
	formBody.Set("client_secret", tokenRequest.ClientSecret)
	formBody.Set("grant_type", tokenRequest.GrantType)
	formBody.Set("scope", tokenRequest.Scope)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.issuerURL, strings.NewReader(formBody.Encode()))
	if err != nil {
		return "", 0, errors.Wrap(err, "failed to create token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", 0, errors.Wrap(err, "failed to request token")
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		return "", 0, errors.Errorf("token request failed: %s: %s", res.Status, string(body))
	}

	respBody, err := io.ReadAll(res.Body)
	if err != nil {
		return "", 0, errors.Wrap(err, "failed to read token response")
	}

	var tokenResponse TokenResponse
	if err := json.Unmarshal(respBody, &tokenResponse); err != nil {
		return "", 0, errors.Wrap(err, "failed to unmarshal token response")
	}
	ttl := time.Duration(tokenResponse.ExpiresIn) * time.Second
	return tokenResponse.AccessToken, ttl, nil
}

// SharedConnector provides common authentication logic for all controllers.
type SharedConnector struct {
	kube      client.Client
	usage     resource.ModernTracker
	newAuthFn func(endpoint, creds, credType, issuerURL string) *AuthManager
	cache     sync.Map
}

// NewSharedConnector creates a new shared connector instance.
func NewSharedConnector(kube client.Client, usage resource.ModernTracker) *SharedConnector {
	return &SharedConnector{
		kube:      kube,
		usage:     usage,
		newAuthFn: NewAuthManager,
		cache:     sync.Map{},
	}
}

// Connect resolves the managed resource's ProviderConfig, tracks that the
// resource is using it, extracts the configured credentials and returns a
// (cached) AuthManager for those credentials.
func (c *SharedConnector) Connect(ctx context.Context, mg resource.ModernManaged) (*AuthManager, error) {
	pc, err := clients.ResolveProviderConfig(ctx, c.kube, c.usage, mg)
	if err != nil {
		return nil, err
	}

	// Cache one AuthManager per ProviderConfig so we don't exchange a new
	// token on every reconcile.
	cacheKey := string(pc.GetUID())
	if cacheKey == "" {
		cacheKey = pc.GetName()
	}
	if manager, ok := c.cache.Load(cacheKey); ok {
		return manager.(*AuthManager), nil
	}

	cd := pc.Spec.Credentials
	data, err := resource.CommonCredentialExtractor(ctx, cd.Source, c.kube, cd.CommonCredentialSelectors)
	if err != nil {
		return nil, errors.Wrap(err, errGetCreds)
	}

	manager := c.newAuthFn(
		pc.Spec.ManagementURI,
		string(data),
		pc.Spec.CredentialsType,
		pc.Spec.OauthIssuerUrl,
	)
	c.cache.Store(cacheKey, manager)
	return manager, nil
}
