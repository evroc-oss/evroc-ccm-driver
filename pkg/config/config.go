// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"

	sdkconfig "github.com/evroc-oss/evroc-go-sdk/config"
	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigPath is where the config file is mounted in the container.
	DefaultConfigPath = "/etc/evroc/config.yaml"

	// StackTypeIPv4Only is the default cluster IP stack: nodes have IPv4 addresses.
	StackTypeIPv4Only = "ipv4-only"

	// StackTypeDualStack means nodes have both IPv4 and IPv6 addresses. The load
	// balancer reaches them over IPv4.
	StackTypeDualStack = "dual-stack"

	// StackTypeIPv6Only means nodes have only IPv6 addresses, so the load balancer
	// must reach backends over IPv6. The load balancer frontend is always IPv4.
	StackTypeIPv6Only = "ipv6-only"
)

// Config holds the CCM configuration.
//
// The auth, api, and context sections mirror the evroc SDK's own configuration
// schema, which is also what the CSI driver reads. That compatibility applies
// to the schema, not the credentials: one Config contains one evroc identity,
// and separate identities and Kubernetes Secrets are recommended for least
// privilege. A shared identity bound to both roles is also valid. The
// loadbalancers section is specific to the CCM.
//
// The CCM identity binds kubernetesCCMAgent; the CSI identity binds
// kubernetesCSIAgent.
type Config struct {
	CCM           CCMConfig          `json:"ccm,omitempty" yaml:"ccm,omitempty"`
	Auth          AuthConfig         `json:"auth" yaml:"auth"`
	API           APIConfig          `json:"api" yaml:"api,omitempty"`
	Context       ContextConfig      `json:"context" yaml:"context"`
	LoadBalancers LoadBalancerConfig `json:"loadbalancers" yaml:"loadbalancers,omitempty"`
}

// CCMConfig controls resource ownership for this installation.
type CCMConfig struct {
	// Identifier scopes ownership of managed cloud resources to this installation.
	// When omitted, the project name is used, matching the CSI driver.
	Identifier string `json:"identifier,omitempty" yaml:"identifier,omitempty"`
}

// CCMIdentifier returns the installation identifier, defaulting to the project.
func (c *Config) CCMIdentifier() string {
	if c.CCM.Identifier != "" {
		return c.CCM.Identifier
	}
	return c.Context.Project
}

// AuthConfig holds the service account credentials used for jwt-bearer
// authentication.
type AuthConfig struct {
	// ServiceAccountID is the bare service account name. ClientID defaults to
	// <service_account_id>_<project>.
	ServiceAccountID string `json:"service_account_id" yaml:"service_account_id"`

	// ServiceAccountSecret is the signing key for jwt-bearer authentication,
	// either a file path or a base64-encoded JWK.
	ServiceAccountSecret string `json:"service_account_secret" yaml:"service_account_secret"`

	// RefreshToken authenticates as a user rather than a service account. It is
	// an alternative to the service account fields above, intended for
	// development and testing: a service account is scoped to its own role,
	// whereas a refresh token carries the full permissions of the user who
	// issued it.
	RefreshToken string `json:"refresh_token,omitempty" yaml:"refresh_token,omitempty"`

	// ClientID is the OAuth2 client identifier. When empty the SDK derives it
	// from the service account ID and project.
	ClientID string `json:"client_id,omitempty" yaml:"client_id,omitempty"`

	// TokenURL overrides the OIDC token endpoint. When empty the SDK default is
	// used.
	TokenURL string `json:"token_url,omitempty" yaml:"token_url,omitempty"`

	// Scopes overrides the OAuth2 scopes. When empty the SDK defaults are used.
	Scopes []string `json:"scopes,omitempty" yaml:"scopes,omitempty"`
}

// APIConfig holds the evroc API endpoint configuration.
type APIConfig struct {
	BaseURL string `json:"base_url,omitempty" yaml:"base_url,omitempty"`
}

// ContextConfig holds the project, region, and organization the CCM operates in.
type ContextConfig struct {
	Organization string `json:"organization" yaml:"organization"`
	Project      string `json:"project" yaml:"project"`
	Region       string `json:"region" yaml:"region"`
}

// LoadBalancerConfig defines load-balancer-specific networking options.
type LoadBalancerConfig struct {
	// BackendNetwork matches the load balancer API's backendNetwork schema. A
	// load balancer is deployed only to the zones listed in Subnets. When nil,
	// backendNetwork is omitted and the platform applies its defaults.
	BackendNetwork *LoadBalancerBackendNetworkConfig `json:"backendNetwork,omitempty" yaml:"backendNetwork,omitempty"`

	// StackType is the IP stack type for the cluster network. When "ipv6-only",
	// load balancers reach backends over IPv6. The load balancer frontend is
	// always IPv4. Defaults to "ipv4-only".
	StackType string `json:"stackType,omitempty" yaml:"stackType,omitempty"`
}

// LoadBalancerBackendNetworkConfig selects the VPC and per-zone subnets to
// which load balancers are attached.
type LoadBalancerBackendNetworkConfig struct {
	VPCID   string                            `json:"vpcID" yaml:"vpcID"`
	Subnets []LoadBalancerBackendSubnetConfig `json:"subnets" yaml:"subnets"`
}

// LoadBalancerBackendSubnetConfig selects one subnet for a zone.
type LoadBalancerBackendSubnetConfig struct {
	Zone     string `json:"zone" yaml:"zone"`
	SubnetID string `json:"subnetID" yaml:"subnetID"`
}

// LoadFromPath loads configuration from a YAML file.
func LoadFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}
	return ParseBytes(data)
}

// ParseBytes parses configuration from raw YAML bytes.
func ParseBytes(data []byte) (*Config, error) {
	cfg := &Config{}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg.applyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) applyDefaults() {
	// Keep SDK-backed defaults in sync with the SDK rather than duplicating
	// them here or relying on evroc.New to fill them later in the mainline path.
	sdkCfg := c.SDKConfig()
	sdkCfg.SetDefaults()
	c.Auth.ClientID = sdkCfg.Auth.ClientID
	c.Auth.TokenURL = sdkCfg.Auth.TokenURL
	c.Auth.Scopes = sdkCfg.Auth.Scopes
	c.API.BaseURL = sdkCfg.API.BaseURL

	if c.LoadBalancers.StackType == "" {
		c.LoadBalancers.StackType = StackTypeIPv4Only
	}
}

// SDKConfig returns the SDK portion of the CCM configuration. ParseBytes
// applies all SDK defaults before callers receive a Config.
func (c *Config) SDKConfig() sdkconfig.Config {
	return sdkconfig.Config{
		Auth: sdkconfig.AuthConfig{
			TokenURL:             c.Auth.TokenURL,
			ClientID:             c.Auth.ClientID,
			Scopes:               c.Auth.Scopes,
			RefreshToken:         c.Auth.RefreshToken,
			ServiceAccountID:     c.Auth.ServiceAccountID,
			ServiceAccountSecret: c.Auth.ServiceAccountSecret,
		},
		API: sdkconfig.APIConfig{
			BaseURL: c.API.BaseURL,
		},
		Context: sdkconfig.ContextConfig{
			Project:      c.Context.Project,
			Region:       c.Context.Region,
			Organization: c.Context.Organization,
		},
	}
}

// Validate checks all required fields are present and valid.
func (c *Config) Validate() error {
	if id := c.CCM.Identifier; id != "" && (len(id) > 63 || !validCCMIdentifierRe.MatchString(id)) {
		return fmt.Errorf("ccm.identifier must be 1-63 alphanumeric characters, hyphens, or underscores, starting and ending with an alphanumeric character")
	}
	// Either a service account or a refresh token, but a service account is
	// what production deployments should use.
	if c.Auth.RefreshToken == "" {
		if c.Auth.ServiceAccountID == "" {
			return fmt.Errorf("auth.service_account_id is required (or set auth.refresh_token)")
		}
		if c.Auth.ServiceAccountSecret == "" {
			return fmt.Errorf("auth.service_account_secret is required (or set auth.refresh_token)")
		}
	}

	if err := validateURL(c.API.BaseURL, "api.base_url"); err != nil {
		return err
	}
	if c.Auth.TokenURL != "" {
		if err := validateURL(c.Auth.TokenURL, "auth.token_url"); err != nil {
			return err
		}
	}

	if c.Context.Organization == "" {
		return fmt.Errorf("context.organization is required")
	}
	if !isValidIdentifier(c.Context.Organization) {
		return fmt.Errorf("context.organization must contain only alphanumeric characters and hyphens")
	}
	if c.Context.Project == "" {
		return fmt.Errorf("context.project is required")
	}
	if !isValidIdentifier(c.Context.Project) {
		return fmt.Errorf("context.project must contain only alphanumeric characters and hyphens")
	}
	if c.Context.Region == "" {
		return fmt.Errorf("context.region is required")
	}
	if !isValidIdentifier(c.Context.Region) {
		return fmt.Errorf("context.region must contain only alphanumeric characters and hyphens")
	}

	if network := c.LoadBalancers.BackendNetwork; network != nil {
		if network.VPCID == "" {
			return fmt.Errorf("loadbalancers.backendNetwork.vpcID is required")
		}
		if !isValidIdentifier(network.VPCID) {
			return fmt.Errorf("loadbalancers.backendNetwork.vpcID must contain only alphanumeric characters and hyphens")
		}
		if len(network.Subnets) == 0 {
			return fmt.Errorf("loadbalancers.backendNetwork.subnets must contain at least one subnet")
		}
		zones := make(map[string]struct{}, len(network.Subnets))
		for i, subnet := range network.Subnets {
			if subnet.Zone == "" {
				return fmt.Errorf("loadbalancers.backendNetwork.subnets[%d].zone is required", i)
			}
			if subnet.SubnetID == "" {
				return fmt.Errorf("loadbalancers.backendNetwork.subnets[%d].subnetID is required", i)
			}
			if !isValidIdentifier(subnet.SubnetID) {
				return fmt.Errorf("loadbalancers.backendNetwork.subnets[%d].subnetID must contain only alphanumeric characters and hyphens", i)
			}
			if _, exists := zones[subnet.Zone]; exists {
				return fmt.Errorf("loadbalancers.backendNetwork.subnets contains duplicate zone %q", subnet.Zone)
			}
			zones[subnet.Zone] = struct{}{}
		}
	}

	switch c.LoadBalancers.StackType {
	case StackTypeIPv4Only, StackTypeDualStack, StackTypeIPv6Only:
	default:
		return fmt.Errorf("loadbalancers.stackType must be one of %q, %q, or %q",
			StackTypeIPv4Only, StackTypeDualStack, StackTypeIPv6Only)
	}

	return nil
}

// SubnetRef returns the configured subnet ID as a fully-qualified resource ref.
func (c *Config) SubnetRef(id string) string {
	return c.qualify("subnets", id)
}

// VPCRef returns the fully-qualified ref of the configured VPC, or the empty
// string when none is configured.
func (c *Config) VPCRef() string {
	if c.LoadBalancers.BackendNetwork == nil {
		return ""
	}
	return c.qualify("virtualPrivateClouds", c.LoadBalancers.BackendNetwork.VPCID)
}

// qualify turns a configured resource ID into a fully-qualified evroc ref.
// The load balancer admission webhook rejects bare names in
// spec.backendNetwork, so IDs configured for readability are expanded here.
func (c *Config) qualify(collection, id string) string {
	return fmt.Sprintf("/networking/projects/%s/regions/%s/%s/%s",
		c.Context.Project, c.Context.Region, collection, id)
}

// clone returns a copy that does not share mutable fields with c.
func (c *Config) clone() Config {
	cloned := *c
	cloned.Auth.Scopes = slices.Clone(c.Auth.Scopes)
	if c.LoadBalancers.BackendNetwork != nil {
		backendNetwork := *c.LoadBalancers.BackendNetwork
		backendNetwork.Subnets = slices.Clone(c.LoadBalancers.BackendNetwork.Subnets)
		cloned.LoadBalancers.BackendNetwork = &backendNetwork
	}
	return cloned
}

// String returns a redacted string representation for logging.
func (c *Config) String() string {
	redacted := c.clone()
	redacted.Auth.ServiceAccountSecret = ""
	redacted.Auth.RefreshToken = ""

	data, err := json.Marshal(redacted)
	if err != nil {
		return `{"error":"failed to render redacted config"}`
	}
	return string(data)
}

func validateURL(urlStr, fieldName string) error {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return fmt.Errorf("%s must be a valid URL: %w", fieldName, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s must be an HTTP or HTTPS URL", fieldName)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s must include a host", fieldName)
	}
	return nil
}

var validCCMIdentifierRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_-]*[a-zA-Z0-9])?$`)

var validIdentifierRe = regexp.MustCompile(`^[a-zA-Z0-9-]+$`)

func isValidIdentifier(id string) bool {
	return validIdentifierRe.MatchString(id)
}
