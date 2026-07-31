// SPDX-License-Identifier: Apache-2.0
// Copyright (c) 2026 evroc

package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	// DefaultConfigPath is where the config file is mounted in the container.
	DefaultConfigPath = "/etc/evroc/config.yaml"

	// DefaultBaseURL is the default evroc API endpoint.
	DefaultBaseURL = "https://api.evroc.com"

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
// schema, which is also what the CSI driver reads, so a config written for one
// component is understood by the other. The network section is specific to the
// CCM and is ignored by components that do not recognise it.
//
// Each component authenticates as its own service account so that it holds only
// the permissions of its own role: the CCM binds kubernetesCCMAgent and the CSI
// driver binds kubernetesCSIAgent.
type Config struct {
	Auth    AuthConfig    `yaml:"auth"`
	API     APIConfig     `yaml:"api,omitempty"`
	Context ContextConfig `yaml:"context"`
	Network NetworkConfig `yaml:"network,omitempty"`
}

// AuthConfig holds the service account credentials used for jwt-bearer
// authentication.
type AuthConfig struct {
	// ServiceAccountID is the service account name, project-qualified as
	// <name>_<project>.
	ServiceAccountID string `yaml:"service_account_id"`

	// ServiceAccountSecret is the signing key for jwt-bearer authentication,
	// either a file path or a base64-encoded JWK.
	ServiceAccountSecret string `yaml:"service_account_secret"`

	// RefreshToken authenticates as a user rather than a service account. It is
	// an alternative to the service account fields above, intended for
	// development and testing: a service account is scoped to its own role,
	// whereas a refresh token carries the full permissions of the user who
	// issued it.
	RefreshToken string `yaml:"refresh_token,omitempty"`

	// ClientID is the OAuth2 client identifier. When empty the SDK derives it
	// from the service account ID and project.
	ClientID string `yaml:"client_id,omitempty"`

	// TokenURL overrides the OIDC token endpoint. When empty the SDK default is
	// used.
	TokenURL string `yaml:"token_url,omitempty"`
}

// APIConfig holds the evroc API endpoint configuration.
type APIConfig struct {
	BaseURL string `yaml:"base_url,omitempty"`
}

// ContextConfig holds the project, region, and organization the CCM operates in.
type ContextConfig struct {
	Organization string `yaml:"organization"`
	Project      string `yaml:"project"`
	Region       string `yaml:"region"`
}

// NetworkConfig defines the VPC, subnet, and IP stack configuration for the
// cluster. When omitted, the project's default VPC with default subnets and
// IPv4-only networking is used. Field names mirror the cluster-api provider's
// NetworkSpec so a cluster and its CCM are configured the same way.
type NetworkConfig struct {
	// VPCRef is the name of a pre-existing VPC to place load balancers in. The
	// VPC must already exist in the evroc project. When omitted, the project's
	// default VPC is used.
	VPCRef string `yaml:"vpcRef,omitempty"`

	// SubnetRefs maps availability zone letters to subnet names within the VPC.
	// When VPCRef is set, SubnetRefs should map every zone the cluster spans.
	// When omitted, the default subnet for each zone is used
	// (default-{region}-{zone}).
	SubnetRefs map[string]string `yaml:"subnetRefs,omitempty"`

	// StackType is the IP stack type for the cluster network. When "ipv6-only",
	// load balancers reach backends over IPv6. The load balancer frontend is
	// always IPv4. Defaults to "ipv4-only".
	StackType string `yaml:"stackType,omitempty"`
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
	if c.API.BaseURL == "" {
		c.API.BaseURL = DefaultBaseURL
	}
	if c.Network.StackType == "" {
		c.Network.StackType = StackTypeIPv4Only
	}
}

// Validate checks all required fields are present and valid.
func (c *Config) Validate() error {
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
		return fmt.Errorf("context.organization must contain only alphanumeric characters, hyphens, and underscores")
	}
	if c.Context.Project == "" {
		return fmt.Errorf("context.project is required")
	}
	if !isValidIdentifier(c.Context.Project) {
		return fmt.Errorf("context.project must contain only alphanumeric characters, hyphens, and underscores")
	}
	if c.Context.Region == "" {
		return fmt.Errorf("context.region is required")
	}

	switch c.Network.StackType {
	case StackTypeIPv4Only, StackTypeDualStack, StackTypeIPv6Only:
	default:
		return fmt.Errorf("network.stackType must be one of %q, %q, or %q",
			StackTypeIPv4Only, StackTypeDualStack, StackTypeIPv6Only)
	}

	return nil
}

// SubnetRefForZone returns the fully-qualified subnet ref to use in the given
// zone, falling back to the default subnet naming convention when none is
// configured.
func (c *Config) SubnetRefForZone(zone string) string {
	name, ok := c.Network.SubnetRefs[zone]
	if !ok || name == "" {
		name = fmt.Sprintf("default-%s-%s", c.Context.Region, zone)
	}
	return c.qualify("subnets", name)
}

// VPCRef returns the fully-qualified ref of the configured VPC, or the empty
// string when none is configured.
func (c *Config) VPCRef() string {
	if c.Network.VPCRef == "" {
		return ""
	}
	return c.qualify("virtualPrivateClouds", c.Network.VPCRef)
}

// qualify turns a bare resource name into a fully-qualified evroc resource ID.
// The load balancer admission webhook rejects bare names in
// spec.backendNetwork, so names configured for readability are expanded here.
// A value that is already qualified is passed through unchanged.
func (c *Config) qualify(collection, name string) string {
	if strings.HasPrefix(name, "/") {
		return name
	}
	return fmt.Sprintf("/networking/projects/%s/regions/%s/%s/%s",
		c.Context.Project, c.Context.Region, collection, name)
}

// String returns a redacted string representation for logging.
func (c *Config) String() string {
	return fmt.Sprintf(
		"Config{Auth: {ServiceAccountID: %s, ServiceAccountSecret: <redacted>, RefreshToken: <redacted>}, API: {BaseURL: %s}, Context: {Org: %s, Project: %s, Region: %s}, Network: {VPCRef: %s, StackType: %s}}",
		c.Auth.ServiceAccountID, c.API.BaseURL,
		c.Context.Organization, c.Context.Project, c.Context.Region,
		c.Network.VPCRef, c.Network.StackType,
	)
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

var validIdentifierRe = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func isValidIdentifier(id string) bool {
	return validIdentifierRe.MatchString(id)
}
