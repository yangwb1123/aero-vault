package auth

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
)

// PolicyEffect is the result of evaluating a policy statement.
type PolicyEffect int

const (
	EffectAllow PolicyEffect = iota
	EffectDeny
	EffectImplicitDeny
)

// Policy is a parsed IAM-style bucket policy document.
type Policy struct {
	Version   string
	Statement []Statement
}

// Statement is a single policy statement.
type Statement struct {
	Effect    string
	Principal map[string]interface{}
	Action    []string
	Resource  []string
	Condition map[string]map[string][]string // e.g. {"IpAddress": {"aws:SourceIp": ["10.0.0.0/8"]}}
}

// s3Action maps user-facing S3 action to a canonical name.
var s3Actions = map[string]string{
	"GetObject":       "s3:GetObject",
	"PutObject":       "s3:PutObject",
	"DeleteObject":    "s3:DeleteObject",
	"ListBucket":      "s3:ListBucket",
	"HeadObject":      "s3:GetObject",
	"s3:GetObject":    "s3:GetObject",
	"s3:PutObject":    "s3:PutObject",
	"s3:DeleteObject": "s3:DeleteObject",
	"s3:ListBucket":   "s3:ListBucket",
}

// ParsePolicy parses a JSON bucket policy. Returns nil when policy is empty.
func ParsePolicy(jsonStr string) (*Policy, error) {
	if jsonStr == "" {
		return nil, nil
	}
	var p Policy
	if err := json.Unmarshal([]byte(jsonStr), &p); err != nil {
		return nil, fmt.Errorf("malformed policy: %w", err)
	}
	if len(p.Statement) == 0 {
		return nil, nil
	}
	for index := range p.Statement {
		if err := p.Statement[index].validateConditions(); err != nil {
			return nil, fmt.Errorf("statement %d: %w", index, err)
		}
	}
	return &p, nil
}

// Eval evaluates the policy for a given S3 action and source IP.
// Resource constraints are intentionally ignored for compatibility with
// callers that do not have a concrete S3 resource. New protocol adapters
// should use EvalResource.
// Returns EffectAllow, EffectDeny, or EffectImplicitDeny.
// Deny always wins over Allow. If no statement matches, EffectImplicitDeny.
func (p *Policy) Eval(action, sourceIP string) PolicyEffect {
	return p.EvalResource(action, "", sourceIP)
}

// EvalResource evaluates the policy for an action, resource ARN, and source IP.
func (p *Policy) EvalResource(action, resource, sourceIP string) PolicyEffect {
	canonAction := s3Actions[action]
	if canonAction == "" {
		canonAction = action
	}

	allow := false
	for _, stmt := range p.Statement {
		if !stmt.matchesAction(canonAction) {
			continue
		}
		if !stmt.matchesResource(resource) {
			continue
		}
		if !stmt.matchesPrincipal() {
			continue
		}
		if !stmt.matchesConditions(sourceIP) {
			continue
		}
		switch stmt.Effect {
		case "Deny":
			return EffectDeny
		case "Allow":
			allow = true
		}
	}
	if allow {
		return EffectAllow
	}
	return EffectImplicitDeny
}

func (s *Statement) matchesResource(resource string) bool {
	if resource == "" || len(s.Resource) == 0 {
		return true
	}
	for _, pattern := range s.Resource {
		if wildcardMatch(pattern, resource) {
			return true
		}
	}
	return false
}

// wildcardMatch implements IAM-style '*' matching. Unlike path.Match, '*'
// may span '/' characters in object keys.
func wildcardMatch(pattern, value string) bool {
	patternIndex, valueIndex := 0, 0
	starIndex, retryIndex := -1, 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) && pattern[patternIndex] == value[valueIndex] {
			patternIndex++
			valueIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			starIndex = patternIndex
			retryIndex = valueIndex
			patternIndex++
			continue
		}
		if starIndex < 0 {
			return false
		}
		patternIndex = starIndex + 1
		retryIndex++
		valueIndex = retryIndex
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

func (s *Statement) matchesAction(action string) bool {
	if len(s.Action) == 0 {
		return false
	}
	for _, a := range s.Action {
		if a == "*" || a == action {
			return true
		}
		// Prefix wildcard: e.g. "s3:*" matches "s3:GetObject".
		if len(a) > 2 && strings.HasSuffix(a, "*") {
			prefix := a[:len(a)-1]
			if strings.HasPrefix(action, prefix) {
				return true
			}
		}
	}
	return false
}

func (s *Statement) matchesPrincipal() bool {
	if len(s.Principal) == 0 {
		return true
	}
	for _, v := range s.Principal {
		for _, candidate := range principalValues(v) {
			if candidate == "*" {
				return true
			}
		}
	}
	return false
}

// principalValues flattens any AWS Principal value into a list of strings.
// A plain string yields a single-element slice; an array yields its elements;
// a map yields its values.
func principalValues(v interface{}) []string {
	switch val := v.(type) {
	case string:
		return []string{val}
	case []interface{}:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case map[string]interface{}:
		out := make([]string, 0, len(val))
		for _, mv := range val {
			if s, ok := mv.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func (s *Statement) matchesConditions(sourceIP string) bool {
	if len(s.Condition) == 0 {
		return true
	}
	for operator, conditions := range s.Condition {
		for key, values := range conditions {
			switch {
			case operator == "IpAddress" && key == "aws:SourceIp":
				if !ipInAnyCIDR(sourceIP, values) {
					return false
				}
			case operator == "NotIpAddress" && key == "aws:SourceIp":
				if ipInAnyCIDR(sourceIP, values) {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

func (s *Statement) validateConditions() error {
	for operator, conditions := range s.Condition {
		if operator != "IpAddress" && operator != "NotIpAddress" {
			return fmt.Errorf("unsupported condition operator %q", operator)
		}
		for key, values := range conditions {
			if key != "aws:SourceIp" || len(values) == 0 {
				return fmt.Errorf("unsupported or empty condition key %q", key)
			}
			for _, value := range values {
				if _, _, err := net.ParseCIDR(value); err != nil {
					return fmt.Errorf("invalid source CIDR %q", value)
				}
			}
		}
	}
	return nil
}

func ipInAnyCIDR(ip string, cidrs []string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	for _, cidr := range cidrs {
		_, cidrNet, err := net.ParseCIDR(cidr)
		if err != nil {
			continue
		}
		if cidrNet.Contains(parsed) {
			return true
		}
	}
	return false
}

// Allowed checks whether the given action is permitted from sourceIP under this
// policy. Returns true only when at least one Allow matches and no Deny matches.
func Allowed(policy *Policy, action, sourceIP string) bool {
	if policy == nil {
		return true
	}
	e := policy.Eval(action, sourceIP)
	return e == EffectAllow
}

// AllowedResource checks whether action is permitted for a concrete S3
// resource ARN. A nil policy means that the bucket has no policy configured.
func AllowedResource(policy *Policy, action, resource, sourceIP string) bool {
	if policy == nil {
		return true
	}
	return policy.EvalResource(action, resource, sourceIP) == EffectAllow
}

// StringOrArray normalises a JSON field that may be a string or []string.
func StringOrArray(v interface{}) []string {
	switch val := v.(type) {
	case string:
		return []string{val}
	case []interface{}:
		out := make([]string, 0, len(val))
		for _, item := range val {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// UnmarshalJSON handles the AWS IAM style where Action and Resource can be
// either a string or an array of strings.
func (s *Statement) UnmarshalJSON(data []byte) error {
	var raw struct {
		Effect    string                            `json:"Effect"`
		Principal interface{}                       `json:"Principal"`
		Action    interface{}                       `json:"Action"`
		Resource  interface{}                       `json:"Resource"`
		Condition map[string]map[string]interface{} `json:"Condition"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	s.Effect = raw.Effect
	s.Action = StringOrArray(raw.Action)
	s.Resource = StringOrArray(raw.Resource)

	// Normalize Principal: can be "*" (string), {"AWS":"*"}, or {"AWS":["arn"]}
	if raw.Principal != nil {
		s.Principal = map[string]interface{}{}
		switch v := raw.Principal.(type) {
		case string:
			if v == "*" {
				s.Principal["*"] = "*"
			}
		case map[string]interface{}:
			s.Principal = v
		}
	}

	s.Condition = make(map[string]map[string][]string)
	for op, conds := range raw.Condition {
		s.Condition[op] = make(map[string][]string)
		for key, val := range conds {
			s.Condition[op][key] = StringOrArray(val)
		}
	}
	return nil
}
