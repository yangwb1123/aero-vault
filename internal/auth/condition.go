package auth

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ConditionFunc evaluates a single condition value against a context value.
type ConditionFunc func(contextValue string) bool

// ConditionOperator is a condition key operator from the policy grammar.
type ConditionOperator string

const (
	// String operators.
	OpStringEquals              ConditionOperator = "StringEquals"
	OpStringNotEquals           ConditionOperator = "StringNotEquals"
	OpStringEqualsIgnoreCase    ConditionOperator = "StringEqualsIgnoreCase"
	OpStringNotEqualsIgnoreCase ConditionOperator = "StringNotEqualsIgnoreCase"
	OpStringLike                ConditionOperator = "StringLike"
	OpStringNotLike             ConditionOperator = "StringNotLike"

	// Numeric operators.
	OpNumericEquals            ConditionOperator = "NumericEquals"
	OpNumericNotEquals         ConditionOperator = "NumericNotEquals"
	OpNumericLessThan          ConditionOperator = "NumericLessThan"
	OpNumericLessThanEquals    ConditionOperator = "NumericLessThanEquals"
	OpNumericGreaterThan       ConditionOperator = "NumericGreaterThan"
	OpNumericGreaterThanEquals ConditionOperator = "NumericGreaterThanEquals"

	// Date operators.
	OpDateEquals      ConditionOperator = "DateEquals"
	OpDateNotEquals   ConditionOperator = "DateNotEquals"
	OpDateLessThan    ConditionOperator = "DateLessThan"
	OpDateGreaterThan ConditionOperator = "DateGreaterThan"

	// IP operators.
	OpIPAddress    ConditionOperator = "IpAddress"
	OpNotIPAddress ConditionOperator = "NotIpAddress"

	// ARN operators.
	OpArnEquals    ConditionOperator = "ArnEquals"
	OpArnNotEquals ConditionOperator = "ArnNotEquals"
	OpArnLike      ConditionOperator = "ArnLike"
	OpArnNotLike   ConditionOperator = "ArnNotLike"

	// Bool operator.
	OpBool ConditionOperator = "Bool"

	// Binary equality operator.
	OpBinaryEquals ConditionOperator = "BinaryEquals"
)

// validOperators is the set of recognized condition operators.
func isValidOperator(op string) bool {
	switch op {
	case "StringEquals", "StringNotEquals", "StringEqualsIgnoreCase", "StringNotEqualsIgnoreCase":
	case "StringLike", "StringNotLike":
	case "NumericEquals", "NumericNotEquals", "NumericLessThan", "NumericLessThanEquals":
	case "NumericGreaterThan", "NumericGreaterThanEquals":
	case "DateEquals", "DateNotEquals", "DateLessThan", "DateGreaterThan":
	case "IpAddress", "NotIpAddress":
	case "ArnEquals", "ArnNotEquals", "ArnLike", "ArnNotLike":
	case "Bool", "BinaryEquals":
	default:
		return false
	}
	return true
}

// SupportedOperators returns all recognized condition operators.
func SupportedOperators() []ConditionOperator {
	return []ConditionOperator{
		OpStringEquals, OpStringNotEquals, OpStringEqualsIgnoreCase, OpStringNotEqualsIgnoreCase,
		OpStringLike, OpStringNotLike,
		OpNumericEquals, OpNumericNotEquals, OpNumericLessThan, OpNumericLessThanEquals,
		OpNumericGreaterThan, OpNumericGreaterThanEquals,
		OpDateEquals, OpDateNotEquals, OpDateLessThan, OpDateGreaterThan,
		OpIPAddress, OpNotIPAddress,
		OpArnEquals, OpArnNotEquals, OpArnLike, OpArnNotLike,
		OpBool, OpBinaryEquals,
	}
}

// ConditionContext holds the runtime values that conditions are evaluated against.
type ConditionContext struct {
	SourceIP        string
	SecureTransport interface{} // bool or string "true"/"false"
	UserAgent       string
	CurrentTime     interface{} // time.Time or string
	Region          string
	ResourceTag     interface{} // map[string]string
	extra           map[string]string
	values          map[string]string
}

func NewConditionContext() *ConditionContext {
	return &ConditionContext{values: map[string]string{}}
}

func (c *ConditionContext) Get(key string) (string, bool) {
	if v, ok := c.lookupValue(key); ok {
		return v, true
	}
	return c.lookupField(key)
}

func (c *ConditionContext) lookupValue(key string) (string, bool) {
	if c.values != nil {
		if v, ok := c.values[key]; ok {
			return v, true
		}
	}
	if c.extra != nil {
		if v, ok := c.extra[key]; ok {
			return v, true
		}
	}
	return "", false
}

func (c *ConditionContext) lookupField(key string) (string, bool) {
	switch key {
	case "aws:SourceIp", "SourceIP":
		if c.SourceIP != "" {
			return c.SourceIP, true
		}
	case "aws:SecureTransport", "SecureTransport":
		if c.SecureTransport != nil {
			return fmt.Sprintf("%v", c.SecureTransport), true
		}
	case "aws:UserAgent", "UserAgent":
		if c.UserAgent != "" {
			return c.UserAgent, true
		}
	case "aws:CurrentTime", "CurrentTime":
		if v := c.formatCurrentTime(); v != "" {
			return v, true
		}
	case "aws:RequestedRegion", "Region":
		if c.Region != "" {
			return c.Region, true
		}
	case "aws:EpochTime", "EpochTime":
		if v := c.formatEpochTime(); v != "" {
			return v, true
		}
	default:
		if v := c.lookupResourceTag(key); v != "" {
			return v, true
		}
	}
	return "", false
}

func (c *ConditionContext) formatCurrentTime() string {
	if c.CurrentTime == nil {
		return ""
	}
	switch v := c.CurrentTime.(type) {
	case time.Time:
		return v.UTC().Format(time.RFC3339)
	default:
		return fmt.Sprintf("%v", v)
	}
}

func (c *ConditionContext) formatEpochTime() string {
	if t, ok := c.CurrentTime.(time.Time); ok {
		return strconv.FormatInt(t.Unix(), 10)
	}
	return ""
}

func (c *ConditionContext) lookupResourceTag(key string) string {
	for _, prefix := range []string{"aws:ResourceTag/", "ResourceTag/"} {
		if strings.HasPrefix(key, prefix) {
			tagKey := strings.TrimPrefix(key, prefix)
			if c.ResourceTag != nil {
				if m, ok := c.ResourceTag.(map[string]string); ok {
					if v, ok := m[tagKey]; ok {
						return v
					}
				}
			}
		}
	}
	return ""
}

func (c *ConditionContext) Set(key, value string) {
	if c.values == nil {
		c.values = map[string]string{}
	}
	c.values[key] = value
}

func (c *ConditionContext) SetExtra(key, value string) {
	if c.extra == nil {
		c.extra = map[string]string{}
	}
	c.extra[key] = value
}

// ConditionBlock is a condition expression ready for compilation.
type ConditionBlock struct {
	Operator   string
	Conditions map[string][]string
}

func ParseConditionBlock(operator string, conditions map[string][]string) (*ConditionBlock, error) {
	if !isValidOperator(operator) {
		return nil, fmt.Errorf("unsupported condition operator: %s", operator)
	}
	if len(conditions) == 0 {
		return nil, fmt.Errorf("empty conditions")
	}
	for _, vals := range conditions {
		if len(vals) == 0 {
			return nil, fmt.Errorf("empty values")
		}
	}
	return &ConditionBlock{Operator: operator, Conditions: conditions}, nil
}

func (cb *ConditionBlock) Compile() (func(ctx ConditionContext) bool, error) {
	if cb == nil {
		return func(_ ConditionContext) bool { return true }, nil
	}
	op := ConditionOperator(cb.Operator)
	// Each condition key is AND'd; multiple values for the same key are OR'd.
	keyClauses := make([]func(ConditionContext) bool, 0)
	for key, values := range cb.Conditions {
		valueFns := make([]ConditionFunc, 0, len(values))
		for _, value := range values {
			fn, err := compileSingleCondition(op, value)
			if err != nil {
				return nil, err
			}
			valueFns = append(valueFns, fn)
		}
		k := key
		keyClauses = append(keyClauses, func(ctx ConditionContext) bool {
			ctxVal, ok := ctx.Get(k)
			if !ok {
				return false
			}
			for _, fn := range valueFns {
				if fn(ctxVal) {
					return true
				}
			}
			return false
		})
	}
	return func(ctx ConditionContext) bool {
		for _, clause := range keyClauses {
			if !clause(ctx) {
				return false
			}
		}
		return true
	}, nil
}

// compileSingleCondition returns a ConditionFunc for the given operator and value.
func compileSingleCondition(op ConditionOperator, value string) (ConditionFunc, error) {
	switch op {
	case OpStringEquals:
		return func(contextValue string) bool { return contextValue == value }, nil
	case OpStringNotEquals:
		return func(contextValue string) bool { return contextValue != value }, nil
	case OpStringEqualsIgnoreCase:
		return func(contextValue string) bool { return strings.EqualFold(contextValue, value) }, nil
	case OpStringNotEqualsIgnoreCase:
		return func(contextValue string) bool { return !strings.EqualFold(contextValue, value) }, nil
	case OpStringLike, OpStringNotLike:
		re, err := globToRegex(value)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern %q: %w", value, err)
		}
		if op == OpStringLike {
			return func(contextValue string) bool { return re.MatchString(contextValue) }, nil
		}
		return func(contextValue string) bool { return !re.MatchString(contextValue) }, nil
	case OpNumericEquals, OpNumericNotEquals, OpNumericLessThan, OpNumericLessThanEquals,
		OpNumericGreaterThan, OpNumericGreaterThanEquals:
		return compileNumericCondition(op, value)
	case OpDateEquals, OpDateNotEquals, OpDateLessThan, OpDateGreaterThan:
		return compileDateCondition(op, value)
	case OpIPAddress, OpNotIPAddress:
		return compileIPMatch(value, op == OpIPAddress), nil
	case OpBool:
		return func(contextValue string) bool { return strings.EqualFold(contextValue, value) }, nil
	case OpArnLike:
		return globToRegexCond(value, false)
	case OpArnNotLike:
		return globToRegexCond(value, true)
	case OpArnEquals:
		return func(contextValue string) bool { return contextValue == value }, nil
	case OpArnNotEquals:
		return func(contextValue string) bool { return contextValue != value }, nil
	case OpBinaryEquals:
		return func(contextValue string) bool { return contextValue == value }, nil
	default:
		return nil, fmt.Errorf("unsupported condition operator: %s", op)
	}
}

func parseNumeric(s string) (float64, error) {
	// Try int64 first, then float64
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return float64(n), nil
	}
	return strconv.ParseFloat(s, 64)
}

func compileNumericCondition(op ConditionOperator, value string) (ConditionFunc, error) {
	target, err := parseNumeric(value)
	if err != nil {
		return nil, fmt.Errorf("invalid numeric value %q: %w", value, err)
	}
	switch op {
	case OpNumericEquals:
		return func(cv string) bool { v, err := parseNumeric(cv); return err == nil && v == target }, nil
	case OpNumericNotEquals:
		return func(cv string) bool { v, err := parseNumeric(cv); return err != nil || v != target }, nil
	case OpNumericLessThan:
		return func(cv string) bool { v, err := parseNumeric(cv); return err == nil && v < target }, nil
	case OpNumericLessThanEquals:
		return func(cv string) bool { v, err := parseNumeric(cv); return err == nil && v <= target }, nil
	case OpNumericGreaterThan:
		return func(cv string) bool { v, err := parseNumeric(cv); return err == nil && v > target }, nil
	case OpNumericGreaterThanEquals:
		return func(cv string) bool { v, err := parseNumeric(cv); return err == nil && v >= target }, nil
	}
	return nil, fmt.Errorf("unknown numeric operator: %s", op)
}

func compileDateCondition(op ConditionOperator, value string) (ConditionFunc, error) {
	target, err := parseDateTime(value)
	if err != nil {
		return nil, fmt.Errorf("invalid date value %q: %w", value, err)
	}
	switch op {
	case OpDateEquals:
		return func(cv string) bool { t, err := parseDateTime(cv); return err == nil && t.Equal(target) }, nil
	case OpDateNotEquals:
		return func(cv string) bool { t, err := parseDateTime(cv); return err != nil || !t.Equal(target) }, nil
	case OpDateLessThan:
		return func(cv string) bool { t, err := parseDateTime(cv); return err == nil && t.Before(target) }, nil
	case OpDateGreaterThan:
		return func(cv string) bool { t, err := parseDateTime(cv); return err == nil && t.After(target) }, nil
	}
	return nil, fmt.Errorf("unknown date operator: %s", op)
}

func compileIPMatch(cidr string, matchInRange bool) ConditionFunc {
	// Try CIDR first (e.g. "10.0.0.0/8"), then bare IP (e.g. "10.0.0.1").
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		// Not a CIDR; treat as a bare IP (e.g. "10.0.0.1" → match exact).
		ip := net.ParseIP(cidr)
		if ip == nil {
			return func(_ string) bool { return false }
		}
		if matchInRange {
			return func(contextValue string) bool {
				v := net.ParseIP(contextValue)
				return v != nil && v.Equal(ip)
			}
		}
		return func(contextValue string) bool {
			v := net.ParseIP(contextValue)
			return v == nil || !v.Equal(ip)
		}
	}
	if matchInRange {
		return func(contextValue string) bool {
			ip := net.ParseIP(contextValue)
			return ip != nil && ipNet.Contains(ip)
		}
	}
	return func(contextValue string) bool {
		ip := net.ParseIP(contextValue)
		return ip == nil || !ipNet.Contains(ip)
	}
}

func parseDateTime(s string) (time.Time, error) {
	// Try epoch seconds first (numeric string)
	if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(secs, 0), nil
	}
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02T15:04:05", // ISO8601 without timezone
		"2006-01-02",          // date only
		time.RFC1123,
		time.RFC1123Z,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as date/time", s)
}

func globToRegexCond(value string, negate bool) (ConditionFunc, error) {
	re, err := globToRegex(value)
	if err != nil {
		return nil, fmt.Errorf("invalid glob %q: %w", value, err)
	}
	if negate {
		return func(cv string) bool { return !re.MatchString(cv) }, nil
	}
	return func(cv string) bool { return re.MatchString(cv) }, nil
}

func globToRegex(pattern string) (*regexp.Regexp, error) {
	re := "^"
	for _, ch := range pattern {
		switch ch {
		case '*':
			re += ".*"
		case '?':
			re += "."
		case '.', '+', '^', '$', '(', ')', '[', ']', '{', '}', '|', '\\':
			re += "\\" + string(ch)
		default:
			re += string(ch)
		}
	}
	re += "$"
	return regexp.Compile(re)
}

// CompileConditionSet compiles a full condition set (AND-of-ORs).
func CompileConditionSet(conditions map[string]map[string][]string) (func(ctx ConditionContext) bool, error) {
	fns := make([]func(ConditionContext) bool, 0)
	for operator, conds := range conditions {
		cb, err := ParseConditionBlock(operator, conds)
		if err != nil {
			return nil, err
		}
		fn, err := cb.Compile()
		if err != nil {
			return nil, err
		}
		fns = append(fns, fn)
	}
	return func(ctx ConditionContext) bool {
		for _, fn := range fns {
			if !fn(ctx) {
				return false
			}
		}
		return true
	}, nil
}
