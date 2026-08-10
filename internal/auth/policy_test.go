package auth

import (
	"testing"
)

func TestParsePolicy_Empty(t *testing.T) {
	p, err := ParsePolicy("")
	if err != nil || p != nil {
		t.Fatalf("expected nil,nil for empty policy, got %v,%v", p, err)
	}
}

func TestParsePolicy_InvalidJSON(t *testing.T) {
	_, err := ParsePolicy("{bad")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParsePolicy_NoStatements(t *testing.T) {
	p, err := ParsePolicy(`{"Version":"2012-10-17"}`)
	if err != nil || p != nil {
		t.Fatalf("expected nil for no statements, got %v,%v", p, err)
	}
}

func TestPolicy_AllowAll(t *testing.T) {
	p, err := ParsePolicy(`{
		"Version": "2012-10-17",
		"Statement": [{"Effect":"Allow","Principal":"*","Action":"*"}]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !Allowed(p, "s3:GetObject", "10.0.0.1") {
		t.Error("expected Allow for wildcard")
	}
	if !Allowed(p, "s3:PutObject", "10.0.0.1") {
		t.Error("expected Allow for wildcard")
	}
}

func TestPolicy_DenyOverridesAllow(t *testing.T) {
	p, err := ParsePolicy(`{
		"Version": "2012-10-17",
		"Statement": [
			{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"},
			{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Condition":{"IpAddress":{"aws:SourceIp":["10.0.0.0/8"]}}}
		]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if Allowed(p, "s3:GetObject", "10.0.0.1") {
		t.Error("expected Deny for IP in blocked range")
	}
	if !Allowed(p, "s3:GetObject", "192.168.0.1") {
		t.Error("expected Allow for IP outside blocked range")
	}
}

func TestPolicy_ImplicitDeny(t *testing.T) {
	p, err := ParsePolicy(`{
		"Version": "2012-10-17",
		"Statement": [{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"}]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if Allowed(p, "s3:PutObject", "10.0.0.1") {
		t.Error("expected implicit Deny for non-listed action")
	}
}

func TestPolicy_NotIpAddress(t *testing.T) {
	p, err := ParsePolicy(`{
		"Version": "2012-10-17",
		"Statement": [
			{"Effect":"Allow","Principal":"*","Action":"s3:*"},
			{"Effect":"Deny","Principal":"*","Action":"s3:*","Condition":{"NotIpAddress":{"aws:SourceIp":["10.0.0.0/8"]}}}
		]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if Allowed(p, "s3:GetObject", "192.168.0.1") {
		t.Error("expected Deny for IP outside allowed range")
	}
	if !Allowed(p, "s3:GetObject", "10.0.0.1") {
		t.Error("expected Allow for IP inside allowed range")
	}
}

func TestPolicy_EvalActions(t *testing.T) {
	tests := []struct {
		action string
		allow  bool
	}{
		{"GetObject", true},
		{"s3:GetObject", true},
		{"PutObject", false},
		{"HeadObject", true}, // maps to s3:GetObject
	}
	p, err := ParsePolicy(`{
		"Version": "2012-10-17",
		"Statement": [{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"}]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, tt := range tests {
		got := Allowed(p, tt.action, "10.0.0.1")
		if got != tt.allow {
			t.Errorf("Allowed(%q) = %v, want %v", tt.action, got, tt.allow)
		}
	}
}

func TestPolicy_ResourceMatching(t *testing.T) {
	p, err := ParsePolicy(`{
		"Version": "2012-10-17",
		"Statement": [{
			"Effect": "Allow",
			"Principal": "*",
			"Action": "s3:GetObject",
			"Resource": [
				"arn:aws:s3:::docs/exact.txt",
				"arn:aws:s3:::docs/public/*"
			]
		}]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tests := []struct {
		resource string
		allowed  bool
	}{
		{"arn:aws:s3:::docs/exact.txt", true},
		{"arn:aws:s3:::docs/public/nested/readme.txt", true},
		{"arn:aws:s3:::docs/private/readme.txt", false},
		{"arn:aws:s3:::other/public/readme.txt", false},
		{"arn:aws:s3:::docs", false},
	}
	for _, tt := range tests {
		got := AllowedResource(p, "s3:GetObject", tt.resource, "10.0.0.1")
		if got != tt.allowed {
			t.Errorf("AllowedResource(%q) = %v, want %v", tt.resource, got, tt.allowed)
		}
	}
}

func TestPolicy_BucketResourceAndExplicitDeny(t *testing.T) {
	p, err := ParsePolicy(`{
		"Version": "2012-10-17",
		"Statement": [
			{"Effect":"Allow","Principal":"*","Action":"s3:*","Resource":"*"},
			{"Effect":"Deny","Principal":"*","Action":"s3:GetObject","Resource":"arn:aws:s3:::docs/private/*"},
			{"Effect":"Deny","Principal":"*","Action":"s3:ListBucket","Resource":"arn:aws:s3:::blocked"}
		]
	}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if AllowedResource(p, "s3:GetObject", "arn:aws:s3:::docs/private/secret.txt", "10.0.0.1") {
		t.Fatal("explicit object-resource Deny must override wildcard Allow")
	}
	if !AllowedResource(p, "s3:GetObject", "arn:aws:s3:::docs/public/readme.txt", "10.0.0.1") {
		t.Fatal("resource-mismatched Deny must not override Allow")
	}
	if AllowedResource(p, "s3:ListBucket", "arn:aws:s3:::blocked", "10.0.0.1") {
		t.Fatal("exact bucket ARN should match Deny")
	}
	if !AllowedResource(p, "s3:ListBucket", "arn:aws:s3:::docs", "10.0.0.1") {
		t.Fatal("different bucket ARN should retain wildcard Allow")
	}
}

func TestParsePolicyRejectsUnsupportedOrInvalidIPConditions(t *testing.T) {
	policies := []string{
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","Condition":{"StringEquals":{"aws:SourceIp":"10.0.0.1"}}}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","Condition":{"IpAddress":{"aws:UserAgent":"10.0.0.0/8"}}}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*","Condition":{"NotIpAddress":{"aws:SourceIp":"invalid"}}}]}`,
	}
	for _, raw := range policies {
		if _, err := ParsePolicy(raw); err == nil {
			t.Fatalf("expected invalid condition rejection: %s", raw)
		}
	}
}

// AC-1: parse must reject any Effect other than exactly "Allow" or "Deny".
func TestParsePolicyRejectsInvalidEffect(t *testing.T) {
	invalid := []string{
		`{"Statement":[{"Effect":"deny","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"ALlow","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Allow ","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"ALLOW","Action":"s3:*"}]}`,
		`{"Statement":[{"Action":"s3:*"}]}`, // Effect 缺失 → ""
	}
	for _, raw := range invalid {
		if _, err := ParsePolicy(raw); err == nil {
			t.Fatalf("expected parse rejection for invalid Effect: %s", raw)
		}
	}
	// 合法 Effect 不受影响（回归保护）
	for _, raw := range []string{
		`{"Statement":[{"Effect":"Allow","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Deny","Action":"s3:*"}]}`,
	} {
		if _, err := ParsePolicy(raw); err != nil {
			t.Fatalf("valid Effect must parse: %s: %v", raw, err)
		}
	}
}

// AC-2: a typo'd Deny must never downgrade to Allow at eval time.
func TestEvalResource_TypoEffectDenyNeverAllows(t *testing.T) {
	principal := map[string]interface{}{"*": "*"}
	p := &Policy{Statement: []Statement{
		{Effect: "Allow", Principal: principal, Action: []string{"s3:GetObject"}},
		{Effect: "Deny ", Principal: principal, Action: []string{"s3:GetObject"}}, // 拼写错误（尾随空格）
	}}
	if got := p.EvalResource("s3:GetObject", "", "10.0.0.1"); got != EffectDeny {
		t.Fatalf("typo'd Deny must not downgrade to Allow: got %v, want EffectDeny", got)
	}
	// 单独一条拼错 Effect 的 Deny：同样不得产出 Allow
	alone := &Policy{Statement: []Statement{
		{Effect: "deny", Principal: principal, Action: []string{"s3:GetObject"}},
	}}
	if got := alone.EvalResource("s3:GetObject", "", "10.0.0.1"); got == EffectAllow {
		t.Fatal("single typo'd Deny must never evaluate to Allow")
	}
}

// AC-3a: an unknown condition operator must be rejected at parse (existing
// behavior, regression-locked) and never skip a Deny at eval time.
func TestDeny_UnknownConditionOperatorNeverSkipped(t *testing.T) {
	raw := `{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"},
		{"Effect":"Deny","Principal":"*","Action":"s3:GetObject",
		 "Condition":{"BogusOp":{"aws:SourceIp":["10.0.0.0/8"]}}}]}`
	if _, err := ParsePolicy(raw); err == nil {
		t.Fatal("unknown condition operator must be rejected at parse")
	}
	// 纵深防御：绕过解析直接构造时，Deny 不得因未知操作符被跳过
	principal := map[string]interface{}{"*": "*"}
	p := &Policy{Statement: []Statement{
		{Effect: "Allow", Principal: principal, Action: []string{"s3:GetObject"}},
		{Effect: "Deny", Principal: principal, Action: []string{"s3:GetObject"},
			Condition: map[string]map[string][]string{"BogusOp": {"aws:SourceIp": {"10.0.0.0/8"}}}},
	}}
	if got := p.EvalResource("s3:GetObject", "", "10.0.0.1"); got == EffectAllow {
		t.Fatal("un-evaluable Deny condition must not be skipped (fail-open)")
	}
}

// AC-3b: a Deny with a non-wildcard principal must never be silently skipped.
// Branch A (parse rejection) satisfies this via the early return; the eval
// guard is covered directly by TestEvalResource_DenyNonWildcardPrincipalFailsClosed.
func TestDeny_NonWildcardPrincipalNeverSkipped(t *testing.T) {
	raw := `{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"},
		{"Effect":"Deny","Principal":{"AWS":"arn:aws:iam::123:root"},"Action":"s3:GetObject"}]}`
	p, err := ParsePolicy(raw)
	if err != nil {
		return // 分支 A：解析期拒绝 —— 满足 AC-3
	}
	// 分支 B：求值期显式 Deny
	if got := p.EvalResource("s3:GetObject", "", "10.0.0.1"); got != EffectDeny {
		t.Fatalf("Deny with non-wildcard principal must be explicit Deny, got %v", got)
	}
	// 对称约束：Allow + 非通配 principal 绝不构成授权（inert Allow 允许，授权禁止）
	p2, _ := ParsePolicy(`{"Statement":[{"Effect":"Allow",
		"Principal":{"AWS":"arn:aws:iam::123:root"},"Action":"s3:GetObject"}]}`)
	if p2 != nil && p2.EvalResource("s3:GetObject", "", "10.0.0.1") == EffectAllow {
		t.Fatal("Allow with non-wildcard principal must never grant")
	}
}

// Branch A: parse must reject any non-wildcard Principal form, while keeping
// "*", {"AWS":"*"}, {"AWS":["*"]} and absent Principal valid.
func TestParsePolicyRejectsNonWildcardPrincipal(t *testing.T) {
	invalid := []string{
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::123:root"},"Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":["*","arn:aws:iam::123:root"]},"Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"s3:*"}]}`,
	}
	for _, raw := range invalid {
		if _, err := ParsePolicy(raw); err == nil {
			t.Fatalf("expected parse rejection for non-wildcard Principal: %s", raw)
		}
	}
	valid := []string{
		`{"Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":"s3:*"}]}`,
		`{"Statement":[{"Effect":"Allow","Action":"s3:*"}]}`, // 缺省 Principal = 通配
	}
	for _, raw := range valid {
		if _, err := ParsePolicy(raw); err != nil {
			t.Fatalf("wildcard Principal must parse: %s: %v", raw, err)
		}
	}
}

// AC-1: EvalResource must honor per-statement Resource constraints. A
// key-scoped Allow grants only keys inside its scope; anything else is an
// implicit deny. This locks the semantics the REST adapter relies on after
// the resource-constraint fix (bucket-policy-rest-resource-v1).
func TestEvalResource_ResourceScopedAllow(t *testing.T) {
	p, err := ParsePolicy(`{"Version":"2012-10-17","Statement":[
		{"Effect":"Allow","Principal":"*","Action":"s3:GetObject",
		 "Resource":["arn:aws:s3:::default/secret/*"]}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	tests := []struct {
		resource string
		want     PolicyEffect
	}{
		{"arn:aws:s3:::default/secret/key1", EffectAllow},       // 命中 Allow 作用域
		{"arn:aws:s3:::default/other/key1", EffectImplicitDeny}, // 作用域外：隐式拒绝
	}
	for _, tt := range tests {
		if got := p.EvalResource("s3:GetObject", tt.resource, "10.0.0.1"); got != tt.want {
			t.Errorf("EvalResource(%q) = %v, want %v", tt.resource, got, tt.want)
		}
	}
}

// wildcardMatch is the semantic core of IAM '*' matching (may span '/'); this
// table locks its behavior directly so a regression (e.g. path.Match
// semantics) cannot silently widen or narrow scoped policies.
func TestWildcardMatch(t *testing.T) {
	tests := []struct {
		pattern, value string
		want           bool
	}{
		{"a*b", "aXb", true},           // '*' spans any chars
		{"a*b", "aX/Yb", true},         // '*' spans '/'
		{"a*b", "a*b", true},           // literal '*' in value matches literally
		{"a*b", "ab", true},            // '*' matches empty
		{"*", "anything/at/all", true}, // lone '*'
		{"*", "", true},                // lone '*' matches empty value
		{"", "", true},                 // empty pattern matches empty value
		{"", "x", false},               // empty pattern never matches non-empty
		{"s3:*", "s3:GetObject", true},
		{"s3:*", "s3:", true},
		{"s3:*", "ec2:RunInstances", false},
		{"arn:aws:s3:::default/*", "arn:aws:s3:::default", false}, // bucket ARN not under '/*'
		{"arn:aws:s3:::default/*", "arn:aws:s3:::default/secret/k", true},
		{"arn:aws:s3:::default", "arn:aws:s3:::default", true},    // exact bucket ARN
		{"arn:aws:s3:::default", "arn:aws:s3:::default/x", false}, // prefix without '*' does not match longer value
		{"a**b", "axxb", true},      // adjacent stars collapse
		{"prefix*", "prefix", true}, // trailing '*' only
		{"prefix*", "prefixsuffix", true},
		{"a*b", "axbxc", false},
	}
	for _, tt := range tests {
		if got := wildcardMatch(tt.pattern, tt.value); got != tt.want {
			t.Errorf("wildcardMatch(%q, %q) = %v, want %v", tt.pattern, tt.value, got, tt.want)
		}
	}
}

// Eval-layer guard (defense in depth): under branch A the AC-3b eval assertion
// is dead code after the parse early-return, so exercise the guard directly.
func TestEvalResource_DenyNonWildcardPrincipalFailsClosed(t *testing.T) {
	p := &Policy{Statement: []Statement{
		{Effect: "Allow", Principal: map[string]interface{}{"*": "*"}, Action: []string{"s3:GetObject"}},
		{Effect: "Deny", Principal: map[string]interface{}{"AWS": "arn:aws:iam::123:root"}, Action: []string{"s3:GetObject"}},
	}}
	if got := p.EvalResource("s3:GetObject", "", "10.0.0.1"); got != EffectDeny {
		t.Fatalf("Deny with non-wildcard principal must be explicit Deny, got %v", got)
	}
	// 对称约束：Allow + 非通配 principal 绝不构成授权
	only := &Policy{Statement: []Statement{
		{Effect: "Allow", Principal: map[string]interface{}{"AWS": "arn:aws:iam::123:root"}, Action: []string{"s3:GetObject"}},
	}}
	if got := only.EvalResource("s3:GetObject", "", "10.0.0.1"); got == EffectAllow {
		t.Fatal("Allow with non-wildcard principal must never grant")
	}
}
