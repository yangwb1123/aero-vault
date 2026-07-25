package auth

import (
	"net"
	"time"
)

// EvalContext holds the evaluation context for a policy authorization check.
type EvalContext struct {
	Principal   string
	Action      string
	ResourceARN string
	CurrentTime time.Time
	Extra       map[string]string
}

// NewEvalContext creates an EvalContext for the given principal, action, and resource.
func NewEvalContext(principal, action, resourceARN string) EvalContext {
	return EvalContext{
		Principal:   principal,
		Action:      action,
		ResourceARN: resourceARN,
		CurrentTime: time.Now(),
		Extra:       map[string]string{},
	}
}

// PolicyDecision is the result of a policy evaluation.
type PolicyDecision struct {
	Allowed bool
	Denied  bool
}

func compileIPMatchV6(cidr string, matchInRange bool) ConditionFunc {
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return func(_ string) bool { return false }
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
