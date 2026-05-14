package jira

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/akmatori/mcp-gateway/internal/cache"
	"github.com/akmatori/mcp-gateway/internal/database"
	"github.com/akmatori/mcp-gateway/internal/ratelimit"
)

// Cache TTL constants
const (
	ConfigCacheTTL    = 5 * time.Minute  // Credentials cache TTL
	ResponseCacheTTL  = 30 * time.Second // Default API response cache TTL
	CacheCleanupTick  = time.Minute      // Background cleanup interval
	SearchCacheTTL    = 15 * time.Second // Issue search results
	IssueCacheTTL     = 30 * time.Second // Issue detail / comments / transitions
	ChangelogCacheTTL = 60 * time.Second // Changelog
	UserCacheTTL      = 60 * time.Second // User search
	ProjectCacheTTL   = 120 * time.Second // Project list/detail
)

// Auth type constants
const (
	AuthTypeCloudBasic   = "cloud_basic"
	AuthTypeServerBearer = "server_bearer"
	AuthTypeBasic        = "basic"
)

// JiraConfig holds Jira connection configuration
type JiraConfig struct {
	URL         string // Jira base URL (without /rest/api/...)
	AuthType    string // cloud_basic, server_bearer, basic
	APIVersion  string // "2" or "3"
	Username    string // Username/email (required for cloud_basic and basic)
	APIToken    string // API token / PAT / password
	AllowWrites bool   // Gate for write methods
	VerifySSL   bool
	Timeout     int
	UseProxy    bool
	ProxyURL    string
}

// JiraTool handles Jira REST API operations
type JiraTool struct {
	logger        *log.Logger
	configCache   *cache.Cache
	responseCache *cache.Cache
	rateLimiter   *ratelimit.Limiter
}

// NewJiraTool creates a new Jira tool with optional rate limiter
func NewJiraTool(logger *log.Logger, limiter *ratelimit.Limiter) *JiraTool {
	return &JiraTool{
		logger:        logger,
		configCache:   cache.New(ConfigCacheTTL, CacheCleanupTick),
		responseCache: cache.New(ResponseCacheTTL, CacheCleanupTick),
		rateLimiter:   limiter,
	}
}

// Stop cleans up cache resources
func (t *JiraTool) Stop() {
	if t.configCache != nil {
		t.configCache.Stop()
	}
	if t.responseCache != nil {
		t.responseCache.Stop()
	}
}

// configCacheKey returns the cache key for config/credentials
func configCacheKey(incidentID string) string {
	return fmt.Sprintf("creds:%s:jira", incidentID)
}

// responseCacheKey returns the cache key for API responses
func responseCacheKey(path string, params interface{}) string {
	paramsJSON, _ := json.Marshal(params)
	hash := sha256.Sum256(paramsJSON)
	return fmt.Sprintf("%s:%s", path, hex.EncodeToString(hash[:8]))
}

// extractLogicalName extracts the optional logical_name from tool arguments.
func extractLogicalName(args map[string]interface{}) string {
	if v, ok := args["logical_name"].(string); ok {
		return v
	}
	return ""
}

// clampTimeout ensures timeout is within a safe range (5-300 seconds), defaulting to 30.
func clampTimeout(timeout int) int {
	if timeout <= 0 {
		return 30
	}
	if timeout < 5 {
		return 5
	}
	if timeout > 300 {
		return 300
	}
	return timeout
}

// clampLimit ensures Jira's maxResults parameter does not exceed the API maximum of 100.
func clampLimit(limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit > 100 {
		return 100
	}
	return limit
}

// apiPath builds a Jira REST API path for the configured API version.
// suffix should start with '/', e.g. "/search" or "/issue/FOO-1".
func apiPath(version, suffix string) string {
	if version == "" {
		version = "3"
	}
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	return "/rest/api/" + version + suffix
}

// requireWrites returns an error when AllowWrites is disabled.
func requireWrites(config *JiraConfig) error {
	if config == nil || !config.AllowWrites {
		return fmt.Errorf("writes disabled for this Jira instance; enable jira_allow_writes to allow")
	}
	return nil
}

// getConfig fetches Jira configuration from the database with caching.
func (t *JiraTool) getConfig(ctx context.Context, incidentID string, logicalName ...string) (*JiraConfig, error) {
	cacheKey := configCacheKey(incidentID)
	if len(logicalName) > 0 && logicalName[0] != "" {
		cacheKey = fmt.Sprintf("creds:logical:%s:%s", "jira", logicalName[0])
	}

	if cached, ok := t.configCache.Get(cacheKey); ok {
		if config, ok := cached.(*JiraConfig); ok {
			t.logger.Printf("Config cache hit for key %s", cacheKey)
			return config, nil
		}
	}

	ln := ""
	if len(logicalName) > 0 {
		ln = logicalName[0]
	}
	creds, err := database.ResolveToolCredentials(ctx, incidentID, "jira", nil, ln)
	if err != nil {
		return nil, fmt.Errorf("failed to get Jira credentials: %w", err)
	}

	config := &JiraConfig{
		AuthType:    AuthTypeCloudBasic,
		APIVersion:  "3",
		VerifySSL:   true,
		Timeout:     30,
		AllowWrites: false,
	}

	settings := creds.Settings

	if u, ok := settings["jira_url"].(string); ok {
		config.URL = strings.TrimRight(u, "/")
	}

	if v, ok := settings["jira_auth_type"].(string); ok && v != "" {
		config.AuthType = v
	}

	if v, ok := settings["jira_api_version"].(string); ok && v != "" {
		config.APIVersion = v
	}

	if v, ok := settings["jira_username"].(string); ok {
		config.Username = v
	}

	if v, ok := settings["jira_api_token"].(string); ok {
		config.APIToken = v
	}

	if v, ok := settings["jira_allow_writes"].(bool); ok {
		config.AllowWrites = v
	}

	if verify, ok := settings["jira_verify_ssl"].(bool); ok {
		config.VerifySSL = verify
	}

	if timeout, ok := settings["jira_timeout"].(float64); ok {
		config.Timeout = int(timeout)
	}

	config.Timeout = clampTimeout(config.Timeout)

	proxySettings := t.getCachedProxySettings(ctx)
	if proxySettings != nil && proxySettings.ProxyURL != "" && proxySettings.JiraEnabled {
		config.UseProxy = true
		config.ProxyURL = proxySettings.ProxyURL
	}

	t.configCache.Set(cacheKey, config)
	t.logger.Printf("Config cached for key %s", cacheKey)

	return config, nil
}

// getCachedProxySettings fetches proxy settings with caching.
func (t *JiraTool) getCachedProxySettings(ctx context.Context) *database.ProxySettings {
	cacheKey := "proxy:settings"
	if cached, ok := t.configCache.Get(cacheKey); ok {
		if settings, ok := cached.(*database.ProxySettings); ok {
			return settings
		}
	}

	proxySettings, err := database.GetProxySettings(ctx)
	if err != nil || proxySettings == nil {
		return nil
	}

	t.configCache.Set(cacheKey, proxySettings)
	return proxySettings
}

// authHeader returns the Authorization header value for the configured auth type.
func authHeader(config *JiraConfig) (string, error) {
	switch config.AuthType {
	case AuthTypeCloudBasic, AuthTypeBasic:
		if config.Username == "" {
			return "", fmt.Errorf("jira_username is required for %s auth", config.AuthType)
		}
		if config.APIToken == "" {
			return "", fmt.Errorf("jira_api_token is required")
		}
		creds := base64.StdEncoding.EncodeToString([]byte(config.Username + ":" + config.APIToken))
		return "Basic " + creds, nil
	case AuthTypeServerBearer:
		if config.APIToken == "" {
			return "", fmt.Errorf("jira_api_token is required")
		}
		return "Bearer " + config.APIToken, nil
	default:
		return "", fmt.Errorf("unsupported jira_auth_type %q (must be cloud_basic, server_bearer, or basic)", config.AuthType)
	}
}

// doRequest performs an HTTP request against the Jira REST API.
func (t *JiraTool) doRequest(ctx context.Context, config *JiraConfig, method, path string, queryParams url.Values, body io.Reader) ([]byte, error) {
	if config.URL == "" {
		return nil, fmt.Errorf("Jira URL not configured")
	}

	// Build auth header before consuming rate limit budget.
	auth, err := authHeader(config)
	if err != nil {
		return nil, err
	}

	if t.rateLimiter != nil {
		if err := t.rateLimiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit wait cancelled: %w", err)
		}
	}

	fullURL := strings.TrimRight(config.URL, "/") + path
	if len(queryParams) > 0 {
		fullURL += "?" + queryParams.Encode()
	}

	t.logger.Printf("Jira API call: %s %s", method, path)

	transport := &http.Transport{
		DisableKeepAlives: true,
	}

	if config.UseProxy && config.ProxyURL != "" {
		proxyURL, err := url.Parse(config.ProxyURL)
		if err != nil {
			t.logger.Printf("Invalid proxy URL: %v, proceeding without proxy", err)
			transport.Proxy = nil
		} else {
			transport.Proxy = http.ProxyURL(proxyURL)
			t.logger.Printf("Jira using proxy: %s", proxyURL.Host)
		}
	} else {
		transport.Proxy = nil
	}

	if !config.VerifySSL {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // User-opt-in via jira_verify_ssl setting
	}

	client := &http.Client{
		Timeout:   time.Duration(config.Timeout) * time.Second,
		Transport: transport,
	}

	httpReq, err := http.NewRequestWithContext(ctx, method, fullURL, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", auth)
	httpReq.Header.Set("Accept", "application/json")
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	const maxResponseBytes = 5 * 1024 * 1024 // 5 MB
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if len(respBody) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d MB limit", maxResponseBytes/(1024*1024))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errMsg := string(respBody)
		if len(errMsg) > 500 {
			errMsg = errMsg[:500] + "... (truncated)"
		}
		return nil, fmt.Errorf("HTTP error %d: %s", resp.StatusCode, errMsg)
	}

	return respBody, nil
}

// cachedGet performs a cached GET request against the Jira REST API.
func (t *JiraTool) cachedGet(ctx context.Context, incidentID, path string, queryParams url.Values, ttl time.Duration, logicalName ...string) ([]byte, error) {
	cacheKey := responseCacheKey(path, queryParams)
	if len(logicalName) > 0 && logicalName[0] != "" {
		cacheKey = fmt.Sprintf("logical:%s:%s", logicalName[0], cacheKey)
	} else {
		cacheKey = fmt.Sprintf("incident:%s:%s", incidentID, cacheKey)
	}

	if cached, ok := t.responseCache.Get(cacheKey); ok {
		if result, ok := cached.([]byte); ok {
			t.logger.Printf("Response cache hit for %s", path)
			return result, nil
		}
	}

	config, err := t.getConfig(ctx, incidentID, logicalName...)
	if err != nil {
		return nil, err
	}

	respBody, err := t.doRequest(ctx, config, http.MethodGet, path, queryParams, nil)
	if err != nil {
		return nil, err
	}

	t.responseCache.SetWithTTL(cacheKey, respBody, ttl)
	t.logger.Printf("Response cached for %s (TTL: %v)", path, ttl)

	return respBody, nil
}
