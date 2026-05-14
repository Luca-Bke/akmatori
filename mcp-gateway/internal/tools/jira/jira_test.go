package jira

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/akmatori/mcp-gateway/internal/ratelimit"
)

// --- Helper functions ---

func testLogger() *log.Logger {
	return log.New(io.Discard, "", 0)
}

// newTestTool creates a JiraTool with an httptest server's URL pre-populated in the config cache.
func newTestTool(t *testing.T, authType string, handler http.HandlerFunc) (*JiraTool, *httptest.Server, *atomic.Int32) {
	t.Helper()
	counter := &atomic.Int32{}
	wrappedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		handler(w, r)
	})
	server := httptest.NewServer(wrappedHandler)

	tool := NewJiraTool(testLogger(), nil)
	config := &JiraConfig{
		URL:        server.URL,
		AuthType:   authType,
		APIVersion: "3",
		Username:   "user@example.com",
		APIToken:   "test-token",
		VerifySSL:  true,
		Timeout:    5,
	}
	tool.configCache.Set(configCacheKey("test-incident"), config)

	t.Cleanup(func() {
		tool.Stop()
		server.Close()
	})

	return tool, server, counter
}

func getTestConfig(tool *JiraTool) *JiraConfig {
	cached, ok := tool.configCache.Get(configCacheKey("test-incident"))
	if !ok {
		return nil
	}
	return cached.(*JiraConfig)
}

// --- Constructor and lifecycle tests ---

func TestNewJiraTool(t *testing.T) {
	tool := NewJiraTool(testLogger(), nil)
	if tool == nil {
		t.Fatal("expected non-nil tool")
	}
	if tool.configCache == nil {
		t.Error("expected non-nil configCache")
	}
	if tool.responseCache == nil {
		t.Error("expected non-nil responseCache")
	}
	if tool.rateLimiter != nil {
		t.Error("expected nil rateLimiter when none provided")
	}
	tool.Stop()
}

func TestNewJiraTool_WithRateLimiter(t *testing.T) {
	limiter := ratelimit.New(10, 20)
	tool := NewJiraTool(testLogger(), limiter)
	defer tool.Stop()

	if tool.rateLimiter == nil {
		t.Error("expected non-nil rateLimiter")
	}
}

func TestStop_DoubleStop(t *testing.T) {
	tool := NewJiraTool(testLogger(), nil)
	tool.Stop()
	tool.Stop() // should not panic
}

// --- Cache key tests ---

func TestConfigCacheKey(t *testing.T) {
	key := configCacheKey("incident-123")
	expected := "creds:incident-123:jira"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestResponseCacheKey_Stability(t *testing.T) {
	params1 := url.Values{"jql": []string{"project=FOO"}}
	params2 := url.Values{"jql": []string{"project=BAR"}}

	key1 := responseCacheKey("/rest/api/3/search", params1)
	key2 := responseCacheKey("/rest/api/3/search", params2)
	key3 := responseCacheKey("/rest/api/3/search", params1)

	if key1 == key2 {
		t.Error("different params should produce different keys")
	}
	if key1 != key3 {
		t.Error("same params should produce same keys")
	}
}

// --- Helper function tests ---

func TestClampTimeout(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"zero is clamped to default", 0, 30},
		{"negative is clamped to default", -5, 30},
		{"too low is clamped to min", 1, 5},
		{"valid 30 kept", 30, 30},
		{"max kept", 300, 300},
		{"over max clamped", 999, 300},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampTimeout(tt.input); got != tt.want {
				t.Errorf("clampTimeout(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestClampLimit(t *testing.T) {
	tests := []struct {
		name  string
		input int
		want  int
	}{
		{"zero stays zero", 0, 0},
		{"negative stays zero", -1, 0},
		{"50 kept", 50, 50},
		{"100 kept", 100, 100},
		{"over max clamped to 100", 250, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampLimit(tt.input); got != tt.want {
				t.Errorf("clampLimit(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractLogicalName(t *testing.T) {
	tests := []struct {
		name string
		args map[string]interface{}
		want string
	}{
		{"present", map[string]interface{}{"logical_name": "prod-jira"}, "prod-jira"},
		{"absent", map[string]interface{}{}, ""},
		{"wrong type", map[string]interface{}{"logical_name": 42}, ""},
		{"nil map", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractLogicalName(tt.args); got != tt.want {
				t.Errorf("extractLogicalName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestApiPath(t *testing.T) {
	tests := []struct {
		name    string
		version string
		suffix  string
		want    string
	}{
		{"v3 search", "3", "/search", "/rest/api/3/search"},
		{"v2 search", "2", "/search", "/rest/api/2/search"},
		{"default to v3", "", "/issue/FOO-1", "/rest/api/3/issue/FOO-1"},
		{"missing slash prepended", "3", "search", "/rest/api/3/search"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apiPath(tt.version, tt.suffix); got != tt.want {
				t.Errorf("apiPath(%q, %q) = %q, want %q", tt.version, tt.suffix, got, tt.want)
			}
		})
	}
}

func TestRequireWrites(t *testing.T) {
	if err := requireWrites(nil); err == nil {
		t.Error("expected error for nil config")
	}

	cfg := &JiraConfig{AllowWrites: false}
	err := requireWrites(cfg)
	if err == nil {
		t.Fatal("expected error when AllowWrites=false")
	}
	if !strings.Contains(err.Error(), "jira_allow_writes") {
		t.Errorf("error should mention jira_allow_writes setting, got: %v", err)
	}
	if !strings.Contains(err.Error(), "writes disabled") {
		t.Errorf("error should mention writes disabled, got: %v", err)
	}

	cfg.AllowWrites = true
	if err := requireWrites(cfg); err != nil {
		t.Errorf("expected no error when AllowWrites=true, got %v", err)
	}
}

// --- authHeader tests ---

func TestAuthHeader_CloudBasic(t *testing.T) {
	cfg := &JiraConfig{
		AuthType: AuthTypeCloudBasic,
		Username: "user@example.com",
		APIToken: "secret-token",
	}
	got, err := authHeader(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedCreds := base64.StdEncoding.EncodeToString([]byte("user@example.com:secret-token"))
	want := "Basic " + expectedCreds
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestAuthHeader_Basic(t *testing.T) {
	cfg := &JiraConfig{
		AuthType: AuthTypeBasic,
		Username: "admin",
		APIToken: "password",
	}
	got, err := authHeader(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedCreds := base64.StdEncoding.EncodeToString([]byte("admin:password"))
	want := "Basic " + expectedCreds
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestAuthHeader_ServerBearer(t *testing.T) {
	cfg := &JiraConfig{
		AuthType: AuthTypeServerBearer,
		APIToken: "PAT-token",
	}
	got, err := authHeader(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Bearer PAT-token"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

func TestAuthHeader_MissingUsername(t *testing.T) {
	cfg := &JiraConfig{
		AuthType: AuthTypeCloudBasic,
		APIToken: "token",
	}
	if _, err := authHeader(cfg); err == nil {
		t.Fatal("expected error for missing username on cloud_basic")
	}
}

func TestAuthHeader_MissingToken(t *testing.T) {
	cases := []*JiraConfig{
		{AuthType: AuthTypeCloudBasic, Username: "u"},
		{AuthType: AuthTypeServerBearer},
		{AuthType: AuthTypeBasic, Username: "u"},
	}
	for _, cfg := range cases {
		if _, err := authHeader(cfg); err == nil {
			t.Errorf("expected error for missing token (auth_type=%s)", cfg.AuthType)
		}
	}
}

func TestAuthHeader_UnsupportedType(t *testing.T) {
	cfg := &JiraConfig{
		AuthType: "oauth2",
		APIToken: "t",
	}
	_, err := authHeader(cfg)
	if err == nil {
		t.Fatal("expected error for unsupported auth type")
	}
	if !strings.Contains(err.Error(), "unsupported jira_auth_type") {
		t.Errorf("error should mention unsupported, got: %v", err)
	}
}

// --- doRequest tests ---

func TestDoRequest_CloudBasicAuthHeader(t *testing.T) {
	tool, _, _ := newTestTool(t, AuthTypeCloudBasic, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		expectedCreds := base64.StdEncoding.EncodeToString([]byte("user@example.com:test-token"))
		if auth != "Basic "+expectedCreds {
			t.Errorf("expected Basic %s, got %q", expectedCreds, auth)
		}
		if r.Header.Get("Accept") != "application/json" {
			t.Errorf("expected Accept: application/json, got %q", r.Header.Get("Accept"))
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	_, err := tool.doRequest(context.Background(), getTestConfig(tool), http.MethodGet, "/rest/api/3/search", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoRequest_ServerBearerAuthHeader(t *testing.T) {
	tool, _, _ := newTestTool(t, AuthTypeServerBearer, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %q", auth)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	_, err := tool.doRequest(context.Background(), getTestConfig(tool), http.MethodGet, "/rest/api/2/search", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoRequest_BasicAuthHeader(t *testing.T) {
	tool, _, _ := newTestTool(t, AuthTypeBasic, func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		expectedCreds := base64.StdEncoding.EncodeToString([]byte("user@example.com:test-token"))
		if auth != "Basic "+expectedCreds {
			t.Errorf("expected Basic %s, got %q", expectedCreds, auth)
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	_, err := tool.doRequest(context.Background(), getTestConfig(tool), http.MethodGet, "/rest/api/2/search", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoRequest_QueryParams(t *testing.T) {
	tool, _, _ := newTestTool(t, AuthTypeCloudBasic, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("jql") != "project=FOO" {
			t.Errorf("expected jql=project=FOO, got %q", r.URL.Query().Get("jql"))
		}
		if r.URL.Query().Get("maxResults") != "10" {
			t.Errorf("expected maxResults=10, got %q", r.URL.Query().Get("maxResults"))
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	params := url.Values{
		"jql":        []string{"project=FOO"},
		"maxResults": []string{"10"},
	}

	_, err := tool.doRequest(context.Background(), getTestConfig(tool), http.MethodGet, "/rest/api/3/search", params, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoRequest_HTTPError(t *testing.T) {
	tool, _, _ := newTestTool(t, AuthTypeCloudBasic, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"errorMessages":["Issue does not exist"]}`)
	})

	_, err := tool.doRequest(context.Background(), getTestConfig(tool), http.MethodGet, "/rest/api/3/issue/MISSING-1", nil, nil)
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "HTTP error 404") {
		t.Errorf("expected 404 error, got: %v", err)
	}
}

func TestDoRequest_NoURL(t *testing.T) {
	tool := NewJiraTool(testLogger(), nil)
	defer tool.Stop()

	cfg := &JiraConfig{
		AuthType: AuthTypeCloudBasic,
		Username: "u",
		APIToken: "t",
	}
	_, err := tool.doRequest(context.Background(), cfg, http.MethodGet, "/rest/api/3/search", nil, nil)
	if err == nil {
		t.Fatal("expected error for missing URL")
	}
	if !strings.Contains(err.Error(), "URL not configured") {
		t.Errorf("expected URL error, got: %v", err)
	}
}

func TestDoRequest_AuthError(t *testing.T) {
	tool := NewJiraTool(testLogger(), nil)
	defer tool.Stop()

	cfg := &JiraConfig{
		URL:      "http://localhost",
		AuthType: AuthTypeCloudBasic,
		APIToken: "t",
		// Missing username — should fail at auth header step
	}
	_, err := tool.doRequest(context.Background(), cfg, http.MethodGet, "/rest/api/3/search", nil, nil)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !strings.Contains(err.Error(), "jira_username") {
		t.Errorf("expected username error, got: %v", err)
	}
}

func TestDoRequest_WithRateLimiter(t *testing.T) {
	limiter := ratelimit.New(100, 100)
	tool, _, counter := newTestTool(t, AuthTypeCloudBasic, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})
	tool.rateLimiter = limiter

	_, err := tool.doRequest(context.Background(), getTestConfig(tool), http.MethodGet, "/rest/api/3/search", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if counter.Load() != 1 {
		t.Errorf("expected 1 request, got %d", counter.Load())
	}
}

// --- cachedGet tests ---

func TestCachedGet_CachesResponse(t *testing.T) {
	callCount := &atomic.Int32{}
	tool, _, _ := newTestTool(t, AuthTypeCloudBasic, func(w http.ResponseWriter, r *http.Request) {
		callCount.Add(1)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"issues":[]}`)
	})

	ctx := context.Background()
	if _, err := tool.cachedGet(ctx, "test-incident", "/rest/api/3/search", nil, SearchCacheTTL); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if _, err := tool.cachedGet(ctx, "test-incident", "/rest/api/3/search", nil, SearchCacheTTL); err != nil {
		t.Fatalf("second call failed: %v", err)
	}
	if callCount.Load() != 1 {
		t.Errorf("expected 1 server call (second cached), got %d", callCount.Load())
	}
}

func TestCachedGet_LogicalNameIsolation(t *testing.T) {
	tool, _, _ := newTestTool(t, AuthTypeCloudBasic, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{}`)
	})

	cfg2 := &JiraConfig{
		URL:        getTestConfig(tool).URL,
		AuthType:   AuthTypeCloudBasic,
		APIVersion: "3",
		Username:   "other@example.com",
		APIToken:   "other-token",
		VerifySSL:  true,
		Timeout:    5,
	}
	tool.configCache.Set(fmt.Sprintf("creds:logical:%s:%s", "jira", "prod-jira"), cfg2)

	ctx := context.Background()
	if _, err := tool.cachedGet(ctx, "test-incident", "/rest/api/3/search", nil, SearchCacheTTL); err != nil {
		t.Fatalf("incident-keyed call failed: %v", err)
	}
	if _, err := tool.cachedGet(ctx, "test-incident", "/rest/api/3/search", nil, SearchCacheTTL, "prod-jira"); err != nil {
		t.Fatalf("logical-name-keyed call failed: %v", err)
	}
}
