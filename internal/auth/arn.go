// Package auth provides ARN (Amazon Resource Name) parsing and matching
// for IAM-style resource-level access control.
//
// ARN format: arn:partition:service:region:account:resource
// AeroVault uses a custom scheme: arn:aero:tenant:<tenant-id>:bucket:<bucket-name>/<key-path>
//
// The matcher supports wildcard patterns in the resource portion using
// * (any sequence) and ? (single character) glob patterns.
package auth

import (
	"fmt"
	"strings"
)

// ARN represents a parsed Amazon Resource Name.
type ARN struct {
	Partition string // "aero" for AeroVault
	Service   string // resource type (e.g. "tenant", "bucket", "object")
	Region    string // (unused in AeroVault, kept for AWS compatibility)
	Account   string // tenant ID
	Resource  string // resource path with optional qualifier
}

// ParseARN parses an ARN string into its components.
//
// Expected format: arn:aero:<service>:<region>:<tenant>:<resource>
//
// Examples:
//
//	arn:aero:tenant:us-east-1:acme:bucket
//	arn:aero:bucket:us-east-1:acme:my-bucket/keys/*
//	arn:aero:object:us-east-1:acme:my-bucket/keys/foo.txt
//
// For compatibility with AWS-style ARNs, the format can also be:
//
//	arn:aws:s3:::my-bucket/*
func ParseARN(s string) (*ARN, error) {
	const minParts = 6
	parts := strings.SplitN(s, ":", minParts)
	if len(parts) < minParts {
		return nil, fmt.Errorf("invalid ARN %q: expected at least %d colon-separated parts, got %d",
			s, minParts, len(parts))
	}
	if parts[0] != "arn" {
		return nil, fmt.Errorf("invalid ARN %q: must start with 'arn'", s)
	}

	arn := &ARN{
		Partition: parts[1],
		Service:   parts[2],
		Region:    parts[3],
		Account:   parts[4],
	}

	// The resource is everything after the 5th colon.
	resourceParts := parts[5:]
	arn.Resource = strings.Join(resourceParts, ":")

	return arn, nil
}

// MatchARN checks whether a given ARN string matches a pattern ARN string.
// The pattern may contain * and ? wildcards in the resource portion.
//
// Matching rules:
//   - Partition, Service, Region, and Account must match exactly.
//   - If the pattern's Region or Account is "*", it matches any value.
//   - The resource portion supports glob matching (* and ?).
//   - A single "*" in the resource portion matches any resource.
//
// Examples:
//
//	MatchARN("arn:aero:bucket:*:acme:my-bucket/*", "arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo")
//	→ true
//
//	MatchARN("arn:aero:bucket:*:*:my-bucket", "arn:aero:bucket:us-east-1:acme:my-bucket")
//	→ true
func MatchARN(pattern, arn string) bool {
	p, err := ParseARN(pattern)
	if err != nil {
		return false
	}
	a, err := ParseARN(arn)
	if err != nil {
		return false
	}

	// Partition must match.
	if p.Partition != a.Partition {
		return false
	}

	// Service must match (wildcard * supported).
	if p.Service != "*" && p.Service != a.Service {
		return false
	}

	// Region: pattern "*" matches any region; otherwise exact match.
	if p.Region != "*" && p.Region != a.Region {
		return false
	}

	// Account: pattern "*" matches any account; otherwise exact match.
	if p.Account != "*" && p.Account != a.Account {
		return false
	}

	// Resource: use glob matching.
	return matchGlob(p.Resource, a.Resource)
}

// matchGlob checks whether a string matches a glob pattern containing
// * (any sequence) and ? (single character). Unlike regex-based glob
// matching, this is a simple backtracking implementation that avoids
// regex compilation overhead.
func matchGlob(pattern, s string) bool {
	px := 0 // pattern index
	sx := 0 // string index
	star := -1
	match := 0

	for sx < len(s) {
		if px < len(pattern) && (pattern[px] == '?' || pattern[px] == s[sx]) {
			px++
			sx++
			continue
		}
		if px < len(pattern) && pattern[px] == '*' {
			star = px
			match = sx
			px++
			continue
		}
		if star != -1 {
			px = star + 1
			match++
			sx = match
			continue
		}
		return false
	}

	// Consume trailing * in pattern.
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}

	return px == len(pattern)
}

// MatchARNList checks whether an ARN matches any pattern in a list.
// This is the standard IAM pattern for matching a request ARN against
// a list of resource ARNs in a policy statement.
func MatchARNList(patterns []string, arn string) bool {
	for _, pattern := range patterns {
		if MatchARN(pattern, arn) {
			return true
		}
	}
	return false
}

// NormalizeARN ensures the ARN has the correct format for AeroVault.
// If the ARN starts with "arn:" but has an unknown partition, it is
// returned as-is. If it doesn't start with "arn:", it is prefixed
// with "arn:aero:" and the service/region/account/resource are inferred.
func NormalizeARN(s string) string {
	if strings.HasPrefix(s, "arn:") {
		return s
	}
	// Attempt to turn a simple path into an ARN.
	// Format: tenant/bucket/key → arn:aero:bucket:*:tenant:bucket/key
	parts := strings.SplitN(s, "/", 3)
	if len(parts) >= 2 {
		return fmt.Sprintf("arn:aero:bucket:*:%s:%s/%s", parts[0], parts[1], strings.Join(parts[2:], "/"))
	}
	return fmt.Sprintf("arn:aero:bucket:*:%s:%s", "default", s)
}

// ResourceARNFromParts constructs a resource ARN from its components.
func ResourceARNFromParts(tenant, bucket, key string) string {
	if key == "" {
		return fmt.Sprintf("arn:aero:bucket:*:%s:%s", tenant, bucket)
	}
	return fmt.Sprintf("arn:aero:bucket:*:%s:%s/%s", tenant, bucket, key)
}

// IsARN returns true if the string looks like an ARN.
func IsARN(s string) bool {
	return strings.HasPrefix(s, "arn:")
}
