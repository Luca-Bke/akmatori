package database

import (
	"testing"
)

func TestHTTPConnector_Validate_Valid(t *testing.T) {
	readOnly := false
	_ = readOnly // used in JSONB below

	connector := &HTTPConnector{
		ToolTypeName: "internal-billing",
		BaseURLField: "base_url",
		AuthConfig: JSONB{
			"method":      "bearer_token",
			"token_field": "api_token",
		},
		Tools: JSONB{
			"tools": []interface{}{
				map[string]interface{}{
					"name":        "get_invoice",
					"description": "Get an invoice by ID",
					"http_method": "GET",
					"path":        "/api/invoices/{{invoice_id}}",
					"params": []interface{}{
						map[string]interface{}{
							"name":     "invoice_id",
							"type":     "string",
							"required": true,
							"in":       "path",
						},
					},
				},
				map[string]interface{}{
					"name":        "list_invoices",
					"description": "List invoices with optional status filter",
					"http_method": "GET",
					"path":        "/api/invoices",
					"params": []interface{}{
						map[string]interface{}{
							"name":     "status",
							"type":     "string",
							"required": false,
							"in":       "query",
							"default":  "all",
						},
					},
				},
			},
		},
	}

	if err := connector.Validate(); err != nil {
		t.Errorf("expected valid connector, got error: %v", err)
	}
}

func TestHTTPConnector_Validate_MissingFields(t *testing.T) {
	tests := []struct {
		name      string
		connector HTTPConnector
		wantErr   string
	}{
		{
			name:      "missing tool_type_name",
			connector: HTTPConnector{BaseURLField: "url", Tools: JSONB{"tools": []interface{}{map[string]interface{}{"name": "t", "http_method": "GET", "path": "/"}}}},
			wantErr:   "tool_type_name is required",
		},
		{
			name:      "missing base_url_field",
			connector: HTTPConnector{ToolTypeName: "test", Tools: JSONB{"tools": []interface{}{map[string]interface{}{"name": "t", "http_method": "GET", "path": "/"}}}},
			wantErr:   "base_url_field is required",
		},
		{
			name:      "no tools defined",
			connector: HTTPConnector{ToolTypeName: "test", BaseURLField: "url", Tools: JSONB{"tools": []interface{}{}}},
			wantErr:   "at least one tool definition is required",
		},
		{
			name:      "nil tools",
			connector: HTTPConnector{ToolTypeName: "test", BaseURLField: "url"},
			wantErr:   "at least one tool definition is required",
		},
		{
			name: "tool missing name",
			connector: HTTPConnector{
				ToolTypeName: "test",
				BaseURLField: "url",
				Tools:        JSONB{"tools": []interface{}{map[string]interface{}{"http_method": "GET", "path": "/"}}},
			},
			wantErr: "tool[0]: name is required",
		},
		{
			name: "invalid http_method",
			connector: HTTPConnector{
				ToolTypeName: "test",
				BaseURLField: "url",
				Tools:        JSONB{"tools": []interface{}{map[string]interface{}{"name": "t", "http_method": "PATCH", "path": "/"}}},
			},
			wantErr: `invalid http_method "PATCH"`,
		},
		{
			name: "tool missing path",
			connector: HTTPConnector{
				ToolTypeName: "test",
				BaseURLField: "url",
				Tools:        JSONB{"tools": []interface{}{map[string]interface{}{"name": "t", "http_method": "GET"}}},
			},
			wantErr: "path is required",
		},
		{
			name: "invalid param in value",
			connector: HTTPConnector{
				ToolTypeName: "test",
				BaseURLField: "url",
				Tools: JSONB{"tools": []interface{}{map[string]interface{}{
					"name": "t", "http_method": "GET", "path": "/",
					"params": []interface{}{map[string]interface{}{"name": "p", "in": "cookie"}},
				}}},
			},
			wantErr: `invalid 'in' value "cookie"`,
		},
		{
			name: "param missing name",
			connector: HTTPConnector{
				ToolTypeName: "test",
				BaseURLField: "url",
				Tools: JSONB{"tools": []interface{}{map[string]interface{}{
					"name": "t", "http_method": "GET", "path": "/",
					"params": []interface{}{map[string]interface{}{"in": "query"}},
				}}},
			},
			wantErr: "param[0]: name is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.connector.Validate()
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !containsSubstring(err.Error(), tt.wantErr) {
				t.Errorf("error %q should contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestHTTPConnector_Validate_DuplicateToolNames(t *testing.T) {
	connector := &HTTPConnector{
		ToolTypeName: "test-api",
		BaseURLField: "url",
		Tools: JSONB{
			"tools": []interface{}{
				map[string]interface{}{"name": "get_data", "http_method": "GET", "path": "/data"},
				map[string]interface{}{"name": "get_data", "http_method": "POST", "path": "/data"},
			},
		},
	}

	err := connector.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate tool names, got nil")
	}
	if !containsSubstring(err.Error(), "duplicate tool name") {
		t.Errorf("error %q should mention duplicate tool name", err.Error())
	}
}

func TestHTTPConnector_GetToolDefs(t *testing.T) {
	readOnly := false
	connector := &HTTPConnector{
		Tools: JSONB{
			"tools": []interface{}{
				map[string]interface{}{
					"name":        "get_users",
					"description": "List users",
					"http_method": "GET",
					"path":        "/api/users",
					"read_only":   true,
					"params": []interface{}{
						map[string]interface{}{
							"name":     "limit",
							"type":     "integer",
							"required": false,
							"in":       "query",
							"default":  float64(10),
						},
					},
				},
				map[string]interface{}{
					"name":        "create_user",
					"http_method": "POST",
					"path":        "/api/users",
					"read_only":   readOnly,
					"params": []interface{}{
						map[string]interface{}{
							"name":     "email",
							"type":     "string",
							"required": true,
							"in":       "body",
						},
					},
				},
			},
		},
	}

	defs, err := connector.GetToolDefs()
	if err != nil {
		t.Fatalf("GetToolDefs() error: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("expected 2 tool defs, got %d", len(defs))
	}

	// Check first tool
	if defs[0].Name != "get_users" {
		t.Errorf("tool[0] name = %q, want %q", defs[0].Name, "get_users")
	}
	if defs[0].HTTPMethod != "GET" {
		t.Errorf("tool[0] method = %q, want %q", defs[0].HTTPMethod, "GET")
	}
	if !defs[0].IsReadOnly() {
		t.Error("tool[0] should be read-only")
	}
	if len(defs[0].Params) != 1 {
		t.Fatalf("tool[0] expected 1 param, got %d", len(defs[0].Params))
	}
	if defs[0].Params[0].Name != "limit" {
		t.Errorf("tool[0] param[0] name = %q, want %q", defs[0].Params[0].Name, "limit")
	}
	if defs[0].Params[0].In != "query" {
		t.Errorf("tool[0] param[0] in = %q, want %q", defs[0].Params[0].In, "query")
	}

	// Check second tool
	if defs[1].Name != "create_user" {
		t.Errorf("tool[1] name = %q, want %q", defs[1].Name, "create_user")
	}
	if defs[1].IsReadOnly() {
		t.Error("tool[1] should not be read-only")
	}
}

func TestHTTPConnector_GetToolDefs_NilTools(t *testing.T) {
	connector := &HTTPConnector{}
	defs, err := connector.GetToolDefs()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if defs != nil {
		t.Errorf("expected nil defs, got %v", defs)
	}
}

func TestHTTPConnector_GetAuthConfig(t *testing.T) {
	tests := []struct {
		name       string
		authConfig JSONB
		wantNil    bool
		wantMethod HTTPConnectorAuthMethod
		wantToken  string
		wantHeader string
	}{
		{
			name:    "nil auth config",
			wantNil: true,
		},
		{
			name: "bearer token",
			authConfig: JSONB{
				"method":      "bearer_token",
				"token_field": "api_token",
			},
			wantMethod: HTTPConnectorAuthBearer,
			wantToken:  "api_token",
		},
		{
			name: "api key with custom header",
			authConfig: JSONB{
				"method":      "api_key",
				"token_field": "api_key",
				"header_name": "X-API-Key",
			},
			wantMethod: HTTPConnectorAuthAPIKey,
			wantToken:  "api_key",
			wantHeader: "X-API-Key",
		},
		{
			name: "basic auth",
			authConfig: JSONB{
				"method":      "basic_auth",
				"token_field": "credentials",
			},
			wantMethod: HTTPConnectorAuthBasic,
			wantToken:  "credentials",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			connector := &HTTPConnector{AuthConfig: tt.authConfig}
			config, err := connector.GetAuthConfig()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantNil {
				if config != nil {
					t.Errorf("expected nil config, got %+v", config)
				}
				return
			}
			if config == nil {
				t.Fatal("expected non-nil config")
			}
			if config.Method != tt.wantMethod {
				t.Errorf("method = %q, want %q", config.Method, tt.wantMethod)
			}
			if config.TokenField != tt.wantToken {
				t.Errorf("token_field = %q, want %q", config.TokenField, tt.wantToken)
			}
			if config.HeaderName != tt.wantHeader {
				t.Errorf("header_name = %q, want %q", config.HeaderName, tt.wantHeader)
			}
		})
	}
}

func TestHTTPConnectorToolDef_IsReadOnly(t *testing.T) {
	tests := []struct {
		name     string
		readOnly *bool
		want     bool
	}{
		{"nil defaults to true", nil, true},
		{"explicit true", boolPtr(true), true},
		{"explicit false", boolPtr(false), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			def := HTTPConnectorToolDef{ReadOnly: tt.readOnly}
			if got := def.IsReadOnly(); got != tt.want {
				t.Errorf("IsReadOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHTTPConnectorAuthMethod_Constants(t *testing.T) {
	if HTTPConnectorAuthBearer != "bearer_token" {
		t.Errorf("HTTPConnectorAuthBearer = %q, want %q", HTTPConnectorAuthBearer, "bearer_token")
	}
	if HTTPConnectorAuthBasic != "basic_auth" {
		t.Errorf("HTTPConnectorAuthBasic = %q, want %q", HTTPConnectorAuthBasic, "basic_auth")
	}
	if HTTPConnectorAuthAPIKey != "api_key" {
		t.Errorf("HTTPConnectorAuthAPIKey = %q, want %q", HTTPConnectorAuthAPIKey, "api_key")
	}
}

func TestHTTPConnector_TableName(t *testing.T) {
	c := HTTPConnector{}
	if c.TableName() != "http_connectors" {
		t.Errorf("TableName() = %q, want %q", c.TableName(), "http_connectors")
	}
}

// helper
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func boolPtr(b bool) *bool {
	return &b
}
