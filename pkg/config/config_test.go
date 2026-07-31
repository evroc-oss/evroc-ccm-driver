// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validYAML() string {
	return `
context:
  organization: "my-org"
  project: "my-project"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`
}

func TestParseBytes_ValidConfig(t *testing.T) {
	cfg, err := ParseBytes([]byte(validYAML()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Context.Organization != "my-org" {
		t.Errorf("organization = %q, want %q", cfg.Context.Organization, "my-org")
	}
	if cfg.Context.Project != "my-project" {
		t.Errorf("project = %q, want %q", cfg.Context.Project, "my-project")
	}
	if cfg.Auth.ServiceAccountID != "ccm-agent" {
		t.Errorf("serviceAccountID = %q, want %q", cfg.Auth.ServiceAccountID, "ccm-agent")
	}
	if cfg.Auth.ServiceAccountSecret != "c2VjcmV0" {
		t.Errorf("serviceAccountSecret = %q, want %q", cfg.Auth.ServiceAccountSecret, "c2VjcmV0")
	}
	if cfg.Context.Region != "se-sto" {
		t.Errorf("region = %q, want %q", cfg.Context.Region, "se-sto")
	}
}

func TestParseBytes_AppliesDefaults(t *testing.T) {
	cfg, err := ParseBytes([]byte(validYAML()))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.API.BaseURL != DefaultBaseURL {
		t.Errorf("base_url = %q, want %q", cfg.API.BaseURL, DefaultBaseURL)
	}
	// token_url has no CCM-side default: when empty the SDK supplies its own.
	if cfg.Auth.TokenURL != "" {
		t.Errorf("token_url = %q, want it left empty for the SDK to default", cfg.Auth.TokenURL)
	}
	if cfg.Network.StackType != StackTypeIPv4Only {
		t.Errorf("stackType = %q, want %q", cfg.Network.StackType, StackTypeIPv4Only)
	}
}

func TestParseBytes_CustomURLs(t *testing.T) {
	yaml := `
api:
  base_url: "https://custom-api.evroc.com"
context:
  organization: "org"
  project: "proj"
  region: "eu-west"
auth:
  token_url: "https://custom-auth.evroc.com/realms/test"
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`
	cfg, err := ParseBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.API.BaseURL != "https://custom-api.evroc.com" {
		t.Errorf("restURL = %q, want custom URL", cfg.API.BaseURL)
	}
	if cfg.Auth.TokenURL != "https://custom-auth.evroc.com/realms/test" {
		t.Errorf("issuerURL = %q, want custom URL", cfg.Auth.TokenURL)
	}
}

func TestParseBytes_InvalidYAML(t *testing.T) {
	_, err := ParseBytes([]byte("{{invalid yaml"))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "failed to parse config") {
		t.Errorf("error = %q, want it to contain 'failed to parse config'", err.Error())
	}
}

func TestParseBytes_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "missing service account ID",
			yaml: `
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "auth.service_account_id is required",
		},
		{
			name: "missing service account secret",
			yaml: `
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
`,
			wantErr: "auth.service_account_secret is required",
		},
		{
			name: "missing organization",
			yaml: `
context:
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "context.organization is required",
		},
		{
			name: "missing project",
			yaml: `
context:
  organization: "org"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "context.project is required",
		},
		{
			name: "missing region",
			yaml: `
context:
  organization: "org"
  project: "proj"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "context.region is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseBytes([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseBytes_InvalidIdentifiers(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "invalid organization",
			yaml: `
context:
  organization: "org with spaces"
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "context.organization must contain only alphanumeric",
		},
		{
			name: "invalid project",
			yaml: `
context:
  organization: "org"
  project: "proj/bad"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "context.project must contain only alphanumeric",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseBytes([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseBytes_InvalidURLs(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "invalid restURL scheme",
			yaml: `
api:
  base_url: "ftp://bad.com"
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "api.base_url must be an HTTP or HTTPS URL",
		},
		{
			name: "invalid issuerURL scheme",
			yaml: `
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  token_url: "ftp://bad.com"
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "auth.token_url must be an HTTP or HTTPS URL",
		},
		{
			name: "restURL missing host",
			yaml: `
api:
  base_url: "https://"
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`,
			wantErr: "api.base_url must include a host",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseBytes([]byte(tt.yaml))
			if err == nil {
				t.Fatalf("expected error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestParseBytes_ValidIdentifiers(t *testing.T) {
	tests := []struct {
		name string
		org  string
		proj string
	}{
		{"alphanumeric", "myorg123", "proj456"},
		{"hyphens", "my-org", "my-project"},
		{"underscores", "my_org", "my_project"},
		{"mixed", "my-org_123", "proj-456_abc"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			yaml := `
context:
  organization: "` + tt.org + `"
  project: "` + tt.proj + `"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`
			_, err := ParseBytes([]byte(yaml))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseBytes_CSISectionsIgnored(t *testing.T) {
	yaml := `
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
csi:
  storageClass: "evroc-block"
  defaultVolumeSize: "10Gi"
`
	cfg, err := ParseBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Context.Organization != "org" {
		t.Errorf("organization = %q, want %q", cfg.Context.Organization, "org")
	}
}

func TestString_RedactsServiceAccountSecret(t *testing.T) {
	cfg := &Config{
		Auth: AuthConfig{
			ServiceAccountID:     "ccm-agent",
			ServiceAccountSecret: "super-secret-signing-key",
		},
		API: APIConfig{BaseURL: "https://api.evroc.com"},
		Context: ContextConfig{
			Organization: "org",
			Project:      "proj",
			Region:       "se-sto",
		},
	}

	s := cfg.String()
	if strings.Contains(s, "super-secret-signing-key") {
		t.Error("String() should not contain the service account secret")
	}
	if !strings.Contains(s, "<redacted>") {
		t.Error("String() should contain '<redacted>'")
	}
	if !strings.Contains(s, "org") {
		t.Error("String() should contain organization")
	}
	if !strings.Contains(s, "ccm-agent") {
		t.Error("String() should contain the service account ID")
	}
}

func TestLoadFromPath_ValidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(validYAML()), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Context.Organization != "my-org" {
		t.Errorf("organization = %q, want %q", cfg.Context.Organization, "my-org")
	}
}

func TestLoadFromPath_FileNotFound(t *testing.T) {
	_, err := LoadFromPath("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !strings.Contains(err.Error(), "failed to read config file") {
		t.Errorf("error = %q, want it to contain 'failed to read config file'", err.Error())
	}
}

func TestLoadFromPath_InvalidContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("{{bad yaml"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFromPath(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestValidate_EmptyConfig(t *testing.T) {
	cfg := &Config{}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty config")
	}
}

func TestParseBytes_HTTPRestURL(t *testing.T) {
	yaml := `
api:
  base_url: "http://localhost:8080"
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
`
	cfg, err := ParseBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.API.BaseURL != "http://localhost:8080" {
		t.Errorf("restURL = %q, want %q", cfg.API.BaseURL, "http://localhost:8080")
	}
}

func TestDefaultConfigPath(t *testing.T) {
	if DefaultConfigPath != "/etc/evroc/config.yaml" {
		t.Errorf("DefaultConfigPath = %q, want %q", DefaultConfigPath, "/etc/evroc/config.yaml")
	}
}

func TestValidate_RejectsUnknownStackType(t *testing.T) {
	yaml := `
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
network:
  stackType: "ipv5-only"
`
	_, err := ParseBytes([]byte(yaml))
	if err == nil {
		t.Fatal("expected an error for an unknown stack type")
	}
	if !strings.Contains(err.Error(), "network.stackType") {
		t.Errorf("error = %q, want it to mention network.stackType", err)
	}
}

func TestParseBytes_NetworkConfig(t *testing.T) {
	yaml := `
context:
  organization: "org"
  project: "proj"
  region: "se-sto"
auth:
  service_account_id: "ccm-agent"
  service_account_secret: "c2VjcmV0"
network:
  vpcRef: "vpc-prod"
  stackType: "ipv6-only"
  subnetRefs:
    a: "subnet-a"
    b: "subnet-b"
`
	cfg, err := ParseBytes([]byte(yaml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Network.VPCRef != "vpc-prod" {
		t.Errorf("vpcRef = %q, want %q", cfg.Network.VPCRef, "vpc-prod")
	}
	if cfg.Network.StackType != StackTypeIPv6Only {
		t.Errorf("stackType = %q, want %q", cfg.Network.StackType, StackTypeIPv6Only)
	}
	want := "/networking/projects/proj/regions/se-sto/subnets/subnet-a"
	if got := cfg.SubnetRefForZone("a"); got != want {
		t.Errorf("SubnetRefForZone(a) = %q, want the fully-qualified %q", got, want)
	}
	wantVPC := "/networking/projects/proj/regions/se-sto/virtualPrivateClouds/vpc-prod"
	if got := cfg.VPCRef(); got != wantVPC {
		t.Errorf("VPCRef() = %q, want the fully-qualified %q", got, wantVPC)
	}
}

func TestSubnetRefForZone_DefaultsWhenUnset(t *testing.T) {
	cfg := &Config{
		Context: ContextConfig{Project: "proj", Region: "se-sto"},
	}
	const pfx = "/networking/projects/proj/regions/se-sto/subnets/"

	// No subnetRefs configured at all.
	if got, want := cfg.SubnetRefForZone("a"), pfx+"default-se-sto-a"; got != want {
		t.Errorf("SubnetRefForZone(a) = %q, want %q", got, want)
	}

	// Configured, but not for the requested zone.
	cfg.Network.SubnetRefs = map[string]string{"a": "subnet-a"}
	if got, want := cfg.SubnetRefForZone("c"), pfx+"default-se-sto-c"; got != want {
		t.Errorf("SubnetRefForZone(c) = %q, want %q", got, want)
	}
}

// TestQualify_PassesThroughAlreadyQualifiedRefs lets an operator configure a
// full resource ID instead of a bare name, without it being qualified twice.
func TestQualify_PassesThroughAlreadyQualifiedRefs(t *testing.T) {
	full := "/networking/projects/other/regions/eu-west/subnets/shared-a"
	cfg := &Config{
		Context: ContextConfig{Project: "proj", Region: "se-sto"},
		Network: NetworkConfig{
			VPCRef:     "/networking/projects/other/regions/eu-west/virtualPrivateClouds/shared",
			SubnetRefs: map[string]string{"a": full},
		},
	}
	if got := cfg.SubnetRefForZone("a"); got != full {
		t.Errorf("SubnetRefForZone(a) = %q, want it passed through unchanged", got)
	}
	if got := cfg.VPCRef(); got != cfg.Network.VPCRef {
		t.Errorf("VPCRef() = %q, want it passed through unchanged", got)
	}
}

// TestVPCRef_EmptyWhenUnconfigured keeps the load balancer on the bootstrap
// subnet when no VPC is set, rather than sending a malformed ref.
func TestVPCRef_EmptyWhenUnconfigured(t *testing.T) {
	cfg := &Config{Context: ContextConfig{Project: "proj", Region: "se-sto"}}
	if got := cfg.VPCRef(); got != "" {
		t.Errorf("VPCRef() = %q, want empty when no VPC is configured", got)
	}
}

// TestDeployExampleParses guards the file users copy to create their secret.
func TestDeployExampleParses(t *testing.T) {
	data, err := os.ReadFile("../../deploy/evroc-config-example.yaml")
	if err != nil {
		t.Fatalf("failed to read example config: %v", err)
	}
	cfg, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("deploy/evroc-config-example.yaml does not parse: %v", err)
	}
	if cfg.Auth.ServiceAccountID == "" {
		t.Error("example config should set auth.service_account_id")
	}
	if cfg.Auth.ServiceAccountID == "" {
		t.Error("example config should set auth.csi.serviceAccountID")
	}
}
