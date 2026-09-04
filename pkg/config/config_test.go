// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const minimalYAML = `
context:
  organization: my-org
  project: my-project
  region: se-sto
auth:
  service_account_id: ccm-agent
  service_account_secret: c2VjcmV0
`

func validConfig() Config {
	return Config{
		Auth: AuthConfig{
			ServiceAccountID:     "ccm-agent",
			ServiceAccountSecret: "c2VjcmV0",
			ClientID:             "ccm-agent_my-project",
			TokenURL:             "https://authn.iam.evroc.com/realms/evroc-customer/protocol/openid-connect/token",
			Scopes:               []string{"openid", "offline_access"},
		},
		API: APIConfig{BaseURL: "https://api.evroc.com"},
		Context: ContextConfig{
			Organization: "my-org",
			Project:      "my-project",
			Region:       "se-sto",
		},
		LoadBalancers: LoadBalancerConfig{StackType: StackTypeIPv4Only},
	}
}

func TestParseBytes(t *testing.T) {
	t.Run("minimal config and defaults", func(t *testing.T) {
		got, err := ParseBytes([]byte(minimalYAML))
		if err != nil {
			t.Fatalf("ParseBytes() error = %v", err)
		}
		want := validConfig()
		if !reflect.DeepEqual(*got, want) {
			t.Errorf("ParseBytes() = %#v, want %#v", *got, want)
		}
	})

	t.Run("custom and foreign sections", func(t *testing.T) {
		data := []byte(`
api:
  base_url: https://custom-api.evroc.com
auth:
  refresh_token: development-token
  token_url: http://localhost:8080/token
context:
  organization: org
  project: proj
  region: eu-west
loadbalancers:
  backendNetwork:
    vpcID: vpc-prod
    subnets:
      - zone: a
        subnetID: subnet-a
  stackType: ipv6-only
csi:
  storageClass: evroc-block
`)
		got, err := ParseBytes(data)
		if err != nil {
			t.Fatalf("ParseBytes() error = %v", err)
		}
		if got.API.BaseURL != "https://custom-api.evroc.com" ||
			got.Auth.TokenURL != "http://localhost:8080/token" ||
			got.LoadBalancers.BackendNetwork == nil ||
			got.LoadBalancers.BackendNetwork.VPCID != "vpc-prod" ||
			got.LoadBalancers.StackType != StackTypeIPv6Only ||
			!reflect.DeepEqual(got.LoadBalancers.BackendNetwork.Subnets, []LoadBalancerBackendSubnetConfig{
				{Zone: "a", SubnetID: "subnet-a"},
			}) {
			t.Errorf("unexpected parsed config: %#v", got)
		}
	})

	t.Run("malformed YAML", func(t *testing.T) {
		_, err := ParseBytes([]byte("{{invalid yaml"))
		if err == nil || !strings.Contains(err.Error(), "failed to parse config") {
			t.Fatalf("ParseBytes() error = %v, want parse error", err)
		}
	})
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "valid service-account config",
		},
		{
			name: "refresh token replaces service-account credentials",
			mutate: func(c *Config) {
				c.Auth = AuthConfig{RefreshToken: "token"}
			},
		},
		{
			name: "service account requires an ID",
			mutate: func(c *Config) {
				c.Auth.ServiceAccountID = ""
			},
			wantErr: "auth.service_account_id is required",
		},
		{
			name: "service account requires a secret",
			mutate: func(c *Config) {
				c.Auth.ServiceAccountSecret = ""
			},
			wantErr: "auth.service_account_secret is required",
		},
		{
			name: "organization is required",
			mutate: func(c *Config) {
				c.Context.Organization = ""
			},
			wantErr: "context.organization is required",
		},
		{
			name: "organization rejects invalid characters",
			mutate: func(c *Config) {
				c.Context.Organization = "org with spaces"
			},
			wantErr: "context.organization must contain only alphanumeric",
		},
		{
			name: "project is required",
			mutate: func(c *Config) {
				c.Context.Project = ""
			},
			wantErr: "context.project is required",
		},
		{
			name: "project rejects invalid characters",
			mutate: func(c *Config) {
				c.Context.Project = "proj/bad"
			},
			wantErr: "context.project must contain only alphanumeric",
		},
		{
			name: "region is required",
			mutate: func(c *Config) {
				c.Context.Region = ""
			},
			wantErr: "context.region is required",
		},
		{
			name: "region rejects invalid characters",
			mutate: func(c *Config) {
				c.Context.Region = "se/sto"
			},
			wantErr: "context.region must contain only alphanumeric",
		},
		{
			name: "API URL requires HTTP or HTTPS",
			mutate: func(c *Config) {
				c.API.BaseURL = "ftp://bad.example"
			},
			wantErr: "api.base_url must be an HTTP or HTTPS URL",
		},
		{
			name: "API URL requires a host",
			mutate: func(c *Config) {
				c.API.BaseURL = "https://"
			},
			wantErr: "api.base_url must include a host",
		},
		{
			name: "token URL requires HTTP or HTTPS",
			mutate: func(c *Config) {
				c.Auth.TokenURL = "ftp://bad.example"
			},
			wantErr: "auth.token_url must be an HTTP or HTTPS URL",
		},
		{
			name: "backend network requires a VPC ID",
			mutate: func(c *Config) {
				c.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{
					Subnets: []LoadBalancerBackendSubnetConfig{
						{Zone: "a", SubnetID: "subnet-a"},
					},
				}
			},
			wantErr: "loadbalancers.backendNetwork.vpcID is required",
		},
		{
			name: "backend network requires at least one subnet",
			mutate: func(c *Config) {
				c.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{
					VPCID: "vpc-prod",
				}
			},
			wantErr: "loadbalancers.backendNetwork.subnets must contain at least one subnet",
		},
		{
			name: "backend network VPC must be an ID rather than a ref",
			mutate: func(c *Config) {
				c.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{
					VPCID: "/networking/projects/project/regions/region/virtualPrivateClouds/vpc-prod",
					Subnets: []LoadBalancerBackendSubnetConfig{
						{Zone: "a", SubnetID: "subnet-a"},
					},
				}
			},
			wantErr: "loadbalancers.backendNetwork.vpcID must contain only alphanumeric",
		},
		{
			name: "backend subnet requires a zone",
			mutate: func(c *Config) {
				c.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{
					VPCID: "vpc-prod",
					Subnets: []LoadBalancerBackendSubnetConfig{
						{SubnetID: "subnet-a"},
					},
				}
			},
			wantErr: "loadbalancers.backendNetwork.subnets[0].zone is required",
		},
		{
			name: "backend subnet requires an ID",
			mutate: func(c *Config) {
				c.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{
					VPCID: "vpc-prod",
					Subnets: []LoadBalancerBackendSubnetConfig{
						{Zone: "a"},
					},
				}
			},
			wantErr: "loadbalancers.backendNetwork.subnets[0].subnetID is required",
		},
		{
			name: "backend subnet rejects invalid ID characters",
			mutate: func(c *Config) {
				c.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{
					VPCID: "vpc-prod",
					Subnets: []LoadBalancerBackendSubnetConfig{
						{Zone: "a", SubnetID: "subnet_bad"},
					},
				}
			},
			wantErr: "loadbalancers.backendNetwork.subnets[0].subnetID must contain only alphanumeric",
		},
		{
			name: "backend network rejects duplicate zones",
			mutate: func(c *Config) {
				c.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{
					VPCID: "vpc-prod",
					Subnets: []LoadBalancerBackendSubnetConfig{
						{Zone: "a", SubnetID: "subnet-a"},
						{Zone: "a", SubnetID: "subnet-other"},
					},
				}
			},
			wantErr: "loadbalancers.backendNetwork.subnets contains duplicate zone",
		},
		{
			name: "stack type rejects unsupported values",
			mutate: func(c *Config) {
				c.LoadBalancers.StackType = "ipv5-only"
			},
			wantErr: "loadbalancers.stackType must be one of",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestIdentifiers(t *testing.T) {
	tests := map[string]bool{
		"myorg123":                             true,
		"my-org":                               true,
		"550e8400-e29b-41d4-a716-446655440000": true,
		"my_org":                               false,
		"my-org_123":                           false,
		"":                                     false,
		"org with gap":                         false,
		"org/path":                             false,
	}
	for id, want := range tests {
		if got := isValidIdentifier(id); got != want {
			t.Errorf("isValidIdentifier(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestResourceRefs(t *testing.T) {
	cfg := validConfig()
	const subnetPrefix = "/networking/projects/my-project/regions/se-sto/subnets/"
	const vpcPrefix = "/networking/projects/my-project/regions/se-sto/virtualPrivateClouds/"

	if got := cfg.SubnetRef("subnet-a"); got != subnetPrefix+"subnet-a" {
		t.Errorf("SubnetRef() = %q, want %q", got, subnetPrefix+"subnet-a")
	}

	for _, tt := range []struct {
		name string
		id   string
		want string
	}{
		{
			name: "unset",
		},
		{
			name: "configured ID",
			id:   "vpc-prod",
			want: vpcPrefix + "vpc-prod",
		},
	} {
		t.Run("VPC "+tt.name, func(t *testing.T) {
			if tt.id == "" {
				cfg.LoadBalancers.BackendNetwork = nil
			} else {
				cfg.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{VPCID: tt.id}
			}
			if got := cfg.VPCRef(); got != tt.want {
				t.Errorf("VPCRef() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStringRedactsCredentials(t *testing.T) {
	cfg := validConfig()
	cfg.Auth.ServiceAccountSecret = "service-account-secret"
	cfg.Auth.RefreshToken = "refresh-token"
	cfg.LoadBalancers.BackendNetwork = &LoadBalancerBackendNetworkConfig{
		VPCID: "vpc-prod",
		Subnets: []LoadBalancerBackendSubnetConfig{
			{Zone: "a", SubnetID: "subnet-a"},
			{Zone: "b", SubnetID: "subnet-b"},
		},
	}

	got := cfg.String()
	for _, secret := range []string{cfg.Auth.ServiceAccountSecret, cfg.Auth.RefreshToken} {
		if strings.Contains(got, secret) {
			t.Errorf("String() exposed credential %q", secret)
		}
	}
	for _, public := range []string{cfg.Auth.ServiceAccountID, cfg.Context.Organization, "subnet-a", "subnet-b"} {
		if !strings.Contains(got, public) {
			t.Errorf("String() = %q, want it to contain %q", got, public)
		}
	}

	var rendered Config
	if err := json.Unmarshal([]byte(got), &rendered); err != nil {
		t.Fatalf("String() returned invalid JSON %q: %v", got, err)
	}
	if rendered.Auth.ServiceAccountSecret != "" || rendered.Auth.RefreshToken != "" {
		t.Errorf("String() retained credentials: %#v", rendered.Auth)
	}
	if !reflect.DeepEqual(rendered.LoadBalancers.BackendNetwork, cfg.LoadBalancers.BackendNetwork) {
		t.Errorf("String() backend network = %v, want %v", rendered.LoadBalancers.BackendNetwork, cfg.LoadBalancers.BackendNetwork)
	}
	if cfg.Auth.ServiceAccountSecret != "service-account-secret" || cfg.Auth.RefreshToken != "refresh-token" {
		t.Error("String() mutated the original credentials")
	}
}

func TestLoadFromPath(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte(minimalYAML), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadFromPath(path)
		if err != nil || cfg.Context.Organization != "my-org" {
			t.Fatalf("LoadFromPath() = %#v, %v", cfg, err)
		}
	})

	t.Run("missing", func(t *testing.T) {
		_, err := LoadFromPath(filepath.Join(t.TempDir(), "missing.yaml"))
		if err == nil || !strings.Contains(err.Error(), "failed to read config file") {
			t.Fatalf("LoadFromPath() error = %v, want read error", err)
		}
	})
}

func TestDeployExampleParses(t *testing.T) {
	data, err := os.ReadFile("../../deploy/evroc-config-example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseBytes(data)
	if err != nil {
		t.Fatalf("example config does not parse: %v", err)
	}
	if cfg.Auth.ServiceAccountID == "" || cfg.Auth.ServiceAccountSecret == "" {
		t.Error("example config must set both service-account credential fields")
	}
}
