package auth

import (
	"strconv"
	"testing"
	"time"
)

func TestParseConditionBlock_EmptyConditions(t *testing.T) {
	_, err := ParseConditionBlock("IpAddress", nil)
	if err == nil {
		t.Fatal("expected error for nil conditions")
	}
	_, err = ParseConditionBlock("IpAddress", map[string][]string{})
	if err == nil {
		t.Fatal("expected error for empty conditions")
	}
}

func TestParseConditionBlock_EmptyValues(t *testing.T) {
	_, err := ParseConditionBlock("IpAddress", map[string][]string{
		"aws:SourceIp": {},
	})
	if err == nil {
		t.Fatal("expected error for empty values")
	}
}

func TestParseConditionBlock_UnsupportedOperator(t *testing.T) {
	_, err := ParseConditionBlock("FakeOperator", map[string][]string{
		"key": {"value"},
	})
	if err == nil {
		t.Fatal("expected error for unsupported operator")
	}
}

func TestCondition_IpAddress_Match(t *testing.T) {
	cb, err := ParseConditionBlock("IpAddress", map[string][]string{
		"aws:SourceIp": {"10.0.0.0/8"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{SourceIP: "10.0.0.1"}) {
		t.Error("expected 10.0.0.1 to match 10.0.0.0/8")
	}
	if !fn(ConditionContext{SourceIP: "10.255.255.255"}) {
		t.Error("expected 10.255.255.255 to match 10.0.0.0/8")
	}
	if fn(ConditionContext{SourceIP: "192.168.0.1"}) {
		t.Error("expected 192.168.0.1 NOT to match 10.0.0.0/8")
	}
}

func TestCondition_IpAddress_PlainIP(t *testing.T) {
	cb, err := ParseConditionBlock("IpAddress", map[string][]string{
		"aws:SourceIp": {"10.0.0.1"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{SourceIP: "10.0.0.1"}) {
		t.Error("expected 10.0.0.1 to match exact IP")
	}
	if fn(ConditionContext{SourceIP: "10.0.0.2"}) {
		t.Error("expected 10.0.0.2 NOT to match 10.0.0.1")
	}
}

func TestCondition_IpAddress_InvalidCIDR(t *testing.T) {
	cb, err := ParseConditionBlock("IpAddress", map[string][]string{
		"aws:SourceIp": {"not-a-cidr"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if fn(ConditionContext{SourceIP: "10.0.0.1"}) {
		t.Error("expected invalid CIDR to never match")
	}
}

func TestCondition_NotIpAddress(t *testing.T) {
	cb, err := ParseConditionBlock("NotIpAddress", map[string][]string{
		"aws:SourceIp": {"10.0.0.0/8"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if fn(ConditionContext{SourceIP: "10.0.0.1"}) {
		t.Error("expected 10.0.0.1 to be excluded by NotIpAddress")
	}
	if !fn(ConditionContext{SourceIP: "192.168.0.1"}) {
		t.Error("expected 192.168.0.1 to pass NotIpAddress")
	}
}

func TestCondition_Bool_True(t *testing.T) {
	cb, err := ParseConditionBlock("Bool", map[string][]string{
		"aws:SecureTransport": {"true"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{SecureTransport: true}) {
		t.Error("expected SecureTransport=true to match")
	}
	if fn(ConditionContext{SecureTransport: false}) {
		t.Error("expected SecureTransport=false NOT to match")
	}
}

func TestCondition_Bool_False(t *testing.T) {
	cb, err := ParseConditionBlock("Bool", map[string][]string{
		"aws:SecureTransport": {"false"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{SecureTransport: false}) {
		t.Error("expected SecureTransport=false to match Bool=false")
	}
	if fn(ConditionContext{SecureTransport: true}) {
		t.Error("expected SecureTransport=true NOT to match Bool=false")
	}
}

func TestCondition_Bool_CaseInsensitive(t *testing.T) {
	cb, err := ParseConditionBlock("Bool", map[string][]string{
		"aws:SecureTransport": {"True"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{SecureTransport: true}) {
		t.Error("expected case-insensitive True to match")
	}
}

func TestCondition_StringEquals(t *testing.T) {
	cb, err := ParseConditionBlock("StringEquals", map[string][]string{
		"aws:UserAgent": {"Mozilla/5.0"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{UserAgent: "Mozilla/5.0"}) {
		t.Error("expected exact UA match")
	}
	if fn(ConditionContext{UserAgent: "curl/7.0"}) {
		t.Error("expected non-matching UA to fail")
	}
}

func TestCondition_StringNotEquals(t *testing.T) {
	cb, err := ParseConditionBlock("StringNotEquals", map[string][]string{
		"aws:UserAgent": {"bad-bot"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{UserAgent: "Mozilla/5.0"}) {
		t.Error("expected non-matching UA to pass StringNotEquals")
	}
	if fn(ConditionContext{UserAgent: "bad-bot"}) {
		t.Error("expected matching UA to fail StringNotEquals")
	}
}

func TestCondition_StringEqualsIgnoreCase(t *testing.T) {
	cb, err := ParseConditionBlock("StringEqualsIgnoreCase", map[string][]string{
		"aws:UserAgent": {"Mozilla"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{UserAgent: "MOZILLA"}) {
		t.Error("expected case-insensitive match")
	}
	if !fn(ConditionContext{UserAgent: "mozilla"}) {
		t.Error("expected lowercase match")
	}
}

func TestCondition_StringLike(t *testing.T) {
	cb, err := ParseConditionBlock("StringLike", map[string][]string{
		"aws:UserAgent": {"Mozilla/*"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{UserAgent: "Mozilla/5.0"}) {
		t.Error("expected glob match")
	}
	if fn(ConditionContext{UserAgent: "curl/7.0"}) {
		t.Error("expected non-matching UA to fail")
	}
}

func TestCondition_StringLike_QuestionMark(t *testing.T) {
	cb, err := ParseConditionBlock("StringLike", map[string][]string{
		"aws:UserAgent": {"Mozilla/?.0"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{UserAgent: "Mozilla/5.0"}) {
		t.Error("expected ? to match single char")
	}
	if fn(ConditionContext{UserAgent: "Mozilla/55.0"}) {
		t.Error("expected ? to match exactly one char")
	}
}

func TestCondition_StringNotLike(t *testing.T) {
	cb, err := ParseConditionBlock("StringNotLike", map[string][]string{
		"aws:UserAgent": {"bad-*"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{UserAgent: "good-agent"}) {
		t.Error("expected non-matching UA to pass StringNotLike")
	}
	if fn(ConditionContext{UserAgent: "bad-agent"}) {
		t.Error("expected matching UA to fail StringNotLike")
	}
}

func TestCondition_NumericEquals(t *testing.T) {
	cb, err := ParseConditionBlock("NumericEquals", map[string][]string{
		"s3:ObjectSize": {"1024"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "1024"}}) {
		t.Error("expected 1024 == 1024")
	}
	if fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "2048"}}) {
		t.Error("expected 1024 != 2048")
	}
}

func TestCondition_NumericLessThan(t *testing.T) {
	cb, err := ParseConditionBlock("NumericLessThan", map[string][]string{
		"s3:ObjectSize": {"5000"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "1024"}}) {
		t.Error("expected 1024 < 5000")
	}
	if fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "5000"}}) {
		t.Error("expected 5000 not < 5000")
	}
	if fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "10000"}}) {
		t.Error("expected 10000 not < 5000")
	}
}

func TestCondition_NumericGreaterThan(t *testing.T) {
	cb, err := ParseConditionBlock("NumericGreaterThan", map[string][]string{
		"s3:ObjectSize": {"100"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "200"}}) {
		t.Error("expected 200 > 100")
	}
	if fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "50"}}) {
		t.Error("expected 50 not > 100")
	}
}

func TestCondition_NumericLessThanEquals(t *testing.T) {
	cb, err := ParseConditionBlock("NumericLessThanEquals", map[string][]string{
		"s3:ObjectSize": {"100"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "100"}}) {
		t.Error("expected 100 <= 100")
	}
	if !fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "50"}}) {
		t.Error("expected 50 <= 100")
	}
	if fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "200"}}) {
		t.Error("expected 200 not <= 100")
	}
}

func TestCondition_NumericGreaterThanEquals(t *testing.T) {
	cb, err := ParseConditionBlock("NumericGreaterThanEquals", map[string][]string{
		"s3:ObjectSize": {"100"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "100"}}) {
		t.Error("expected 100 >= 100")
	}
	if !fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "200"}}) {
		t.Error("expected 200 >= 100")
	}
	if fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "50"}}) {
		t.Error("expected 50 not >= 100")
	}
}

func TestCondition_NumericNotEquals(t *testing.T) {
	cb, err := ParseConditionBlock("NumericNotEquals", map[string][]string{
		"s3:ObjectSize": {"1024"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "2048"}}) {
		t.Error("expected 2048 != 1024")
	}
	if fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "1024"}}) {
		t.Error("expected 1024 == 1024 to fail NotEquals")
	}
}

func TestCondition_Numeric_InvalidContext(t *testing.T) {
	cb, err := ParseConditionBlock("NumericEquals", map[string][]string{
		"s3:ObjectSize": {"1024"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if fn(ConditionContext{extra: map[string]string{"s3:ObjectSize": "not-a-number"}}) {
		t.Error("expected non-numeric context to fail")
	}
}

func TestCondition_DateEquals(t *testing.T) {
	cb, err := ParseConditionBlock("DateEquals", map[string][]string{
		"aws:CurrentTime": {"2026-07-12T00:00:00Z"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if !fn(ConditionContext{CurrentTime: now}) {
		t.Error("expected matching date to pass")
	}
	if fn(ConditionContext{CurrentTime: now.Add(24 * time.Hour)}) {
		t.Error("expected non-matching date to fail")
	}
}

func TestCondition_DateLessThan(t *testing.T) {
	cb, err := ParseConditionBlock("DateLessThan", map[string][]string{
		"aws:CurrentTime": {"2026-07-15T00:00:00Z"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	before := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	after := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	if !fn(ConditionContext{CurrentTime: before}) {
		t.Error("expected earlier date to pass DateLessThan")
	}
	if fn(ConditionContext{CurrentTime: after}) {
		t.Error("expected later date to fail DateLessThan")
	}
}

func TestCondition_DateGreaterThan(t *testing.T) {
	cb, err := ParseConditionBlock("DateGreaterThan", map[string][]string{
		"aws:CurrentTime": {"2026-07-01T00:00:00Z"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	after := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	before := time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC)
	if !fn(ConditionContext{CurrentTime: after}) {
		t.Error("expected later date to pass DateGreaterThan")
	}
	if fn(ConditionContext{CurrentTime: before}) {
		t.Error("expected earlier date to fail DateGreaterThan")
	}
}

func TestCondition_Date_EpochSeconds(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	epoch := strconv.FormatInt(now.Unix(), 10)
	cb, err := ParseConditionBlock("DateEquals", map[string][]string{
		"aws:EpochTime": {epoch},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{CurrentTime: now}) {
		t.Error("expected epoch seconds match")
	}
}

func TestCondition_ArnEquals(t *testing.T) {
	cb, err := ParseConditionBlock("ArnEquals", map[string][]string{
		"aws:SourceArn": {"arn:aws:s3:::my-bucket"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"aws:SourceArn": "arn:aws:s3:::my-bucket"}}) {
		t.Error("expected exact ARN match")
	}
	if fn(ConditionContext{extra: map[string]string{"aws:SourceArn": "arn:aws:s3:::other-bucket"}}) {
		t.Error("expected different ARN to fail")
	}
}

func TestCondition_ArnLike(t *testing.T) {
	cb, err := ParseConditionBlock("ArnLike", map[string][]string{
		"aws:SourceArn": {"arn:aws:s3:::my-bucket/*"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"aws:SourceArn": "arn:aws:s3:::my-bucket/keys/foo"}}) {
		t.Error("expected wildcard ARN match")
	}
	if fn(ConditionContext{extra: map[string]string{"aws:SourceArn": "arn:aws:s3:::other-bucket/keys/foo"}}) {
		t.Error("expected non-matching ARN to fail")
	}
}

func TestCondition_AndAcrossKeys(t *testing.T) {
	// Both conditions must pass (AND semantics across keys).
	cb, err := ParseConditionBlock("IpAddress", map[string][]string{
		"aws:SourceIp": {"10.0.0.0/8"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// Single condition key, so both must pass (trivially).
	if !fn(ConditionContext{SourceIP: "10.0.0.1"}) {
		t.Error("expected IP in range")
	}
}

func TestCondition_OrWithinKey(t *testing.T) {
	// Multiple values for the same key → OR semantics.
	cb, err := ParseConditionBlock("IpAddress", map[string][]string{
		"aws:SourceIp": {"10.0.0.0/8", "192.168.0.0/16"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{SourceIP: "10.0.0.1"}) {
		t.Error("expected 10.x to match first CIDR")
	}
	if !fn(ConditionContext{SourceIP: "192.168.0.1"}) {
		t.Error("expected 192.168.x to match second CIDR")
	}
	if fn(ConditionContext{SourceIP: "172.16.0.1"}) {
		t.Error("expected 172.16.x NOT to match any")
	}
}

func TestCondition_MissingContextKey(t *testing.T) {
	cb, err := ParseConditionBlock("StringEquals", map[string][]string{
		"aws:UserAgent": {"test"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// UserAgent not set → condition fails
	if fn(ConditionContext{}) {
		t.Error("expected missing context key to fail")
	}
}

func TestCondition_MissingBoolKeyDefaultsFalse(t *testing.T) {
	cb, err := ParseConditionBlock("Bool", map[string][]string{
		"aws:MultiFactorAuthPresent": {"true"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	// MultiFactorAuthPresent not set → defaults to false → fails
	if fn(ConditionContext{}) {
		t.Error("expected missing Bool key to default to false")
	}
}

func TestCondition_ResourceTag(t *testing.T) {
	cb, err := ParseConditionBlock("StringEquals", map[string][]string{
		"aws:ResourceTag/Environment": {"production"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	ctx := ConditionContext{
		ResourceTag: map[string]string{"Environment": "production"},
	}
	if !fn(ctx) {
		t.Error("expected resource tag match")
	}
	ctx2 := ConditionContext{
		ResourceTag: map[string]string{"Environment": "staging"},
	}
	if fn(ctx2) {
		t.Error("expected non-matching resource tag to fail")
	}
}

func TestCompileConditionSet_Empty(t *testing.T) {
	fn, err := CompileConditionSet(nil)
	if err != nil {
		t.Fatalf("CompileConditionSet(nil): %v", err)
	}
	if !fn(ConditionContext{}) {
		t.Error("expected empty condition set to always pass")
	}
	fn, err = CompileConditionSet(map[string]map[string][]string{})
	if err != nil {
		t.Fatalf("CompileConditionSet(empty): %v", err)
	}
	if !fn(ConditionContext{}) {
		t.Error("expected empty condition set to always pass")
	}
}

func TestCompileConditionSet_MultipleBlocks(t *testing.T) {
	// Both IpAddress AND Bool must pass.
	conditions := map[string]map[string][]string{
		"IpAddress": {"aws:SourceIp": {"10.0.0.0/8"}},
		"Bool":      {"aws:SecureTransport": {"true"}},
	}
	fn, err := CompileConditionSet(conditions)
	if err != nil {
		t.Fatalf("CompileConditionSet: %v", err)
	}
	// Both pass
	if !fn(ConditionContext{SourceIP: "10.0.0.1", SecureTransport: true}) {
		t.Error("expected both conditions to pass")
	}
	// IP fails
	if fn(ConditionContext{SourceIP: "192.168.0.1", SecureTransport: true}) {
		t.Error("expected IP condition to fail")
	}
	// Bool fails
	if fn(ConditionContext{SourceIP: "10.0.0.1", SecureTransport: false}) {
		t.Error("expected Bool condition to fail")
	}
}

func TestCompileConditionSet_InvalidOperator(t *testing.T) {
	conditions := map[string]map[string][]string{
		"NonExistent": {"key": {"value"}},
	}
	_, err := CompileConditionSet(conditions)
	if err == nil {
		t.Fatal("expected error for invalid operator")
	}
}

func TestCondition_StringLike_SpecialRegexChars(t *testing.T) {
	cb, err := ParseConditionBlock("StringLike", map[string][]string{
		"key": {"file(v1).txt"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"key": "file(v1).txt"}}) {
		t.Error("expected literal match with regex-special chars")
	}
}

func TestCondition_StringLike_EmptyPattern(t *testing.T) {
	cb, err := ParseConditionBlock("StringLike", map[string][]string{
		"key": {""},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"key": ""}}) {
		t.Error("expected empty pattern to match empty string")
	}
	if fn(ConditionContext{extra: map[string]string{"key": "a"}}) {
		t.Error("expected empty pattern NOT to match non-empty string")
	}
}

func TestCondition_Numeric_FloatValues(t *testing.T) {
	cb, err := ParseConditionBlock("NumericLessThan", map[string][]string{
		"key": {"10.5"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"key": "10.4"}}) {
		t.Error("expected 10.4 < 10.5")
	}
	if fn(ConditionContext{extra: map[string]string{"key": "10.5"}}) {
		t.Error("expected 10.5 not < 10.5")
	}
	if fn(ConditionContext{extra: map[string]string{"key": "10.6"}}) {
		t.Error("expected 10.6 not < 10.5")
	}
}

func TestCondition_Date_ISO8601_NoTimezone(t *testing.T) {
	// AWS supports ISO 8601 without timezone (treated as UTC).
	cb, err := ParseConditionBlock("DateEquals", map[string][]string{
		"aws:CurrentTime": {"2026-07-12T00:00:00"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if !fn(ConditionContext{CurrentTime: now}) {
		t.Error("expected ISO 8601 without timezone to match UTC")
	}
}

func TestCondition_Date_DateOnly(t *testing.T) {
	cb, err := ParseConditionBlock("DateEquals", map[string][]string{
		"aws:CurrentTime": {"2026-07-12"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	if !fn(ConditionContext{CurrentTime: now}) {
		t.Error("expected date-only format to match")
	}
}

func TestCondition_Date_InvalidFormat(t *testing.T) {
	cb, err := ParseConditionBlock("DateEquals", map[string][]string{
		"aws:CurrentTime": {"not-a-date"},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	_, err = cb.Compile()
	if err == nil {
		t.Fatal("expected error for invalid date format")
	}
}

func TestConditionSet_MatchesAWSExample(t *testing.T) {
	// AWS IAM example: Allow access only from 10.0.0.0/8 and only over HTTPS.
	conditions := map[string]map[string][]string{
		"IpAddress": {"aws:SourceIp": {"10.0.0.0/8"}},
		"Bool":      {"aws:SecureTransport": {"true"}},
	}
	fn, err := CompileConditionSet(conditions)
	if err != nil {
		t.Fatalf("CompileConditionSet: %v", err)
	}
	ctx := ConditionContext{
		SourceIP:        "10.0.0.1",
		SecureTransport: true,
	}
	if !fn(ctx) {
		t.Error("expected AWS example condition to pass")
	}
}

func TestNewEvalContext(t *testing.T) {
	ctx := NewEvalContext("user/admin", "s3:GetObject", "arn:aero:tenant:default:bucket:my-bucket/key")
	if ctx.Principal != "user/admin" {
		t.Errorf("Principal = %q, want %q", ctx.Principal, "user/admin")
	}
	if ctx.Action != "s3:GetObject" {
		t.Errorf("Action = %q, want %q", ctx.Action, "s3:GetObject")
	}
	if ctx.ResourceARN != "arn:aero:tenant:default:bucket:my-bucket/key" {
		t.Errorf("ResourceARN = %q", ctx.ResourceARN)
	}
	if ctx.CurrentTime.IsZero() {
		t.Error("expected CurrentTime to be set")
	}
}

func TestConditionContext_Get(t *testing.T) {
	ctx := ConditionContext{
		SourceIP:        "10.0.0.1",
		SecureTransport: true,
		Region:          "us-east-1",
		CurrentTime:     time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC),
	}
	tests := []struct {
		key   string
		value string
		ok    bool
	}{
		{"aws:SourceIp", "10.0.0.1", true},
		{"aws:SecureTransport", "true", true},
		{"aws:RequestedRegion", "us-east-1", true},
		{"aws:CurrentTime", "2026-07-12T00:00:00Z", true},
		{"aws:NonExistent", "", false},
	}
	for _, tt := range tests {
		v, ok := ctx.Get(tt.key)
		if ok != tt.ok || (ok && v != tt.value) {
			t.Errorf("Get(%q) = (%q, %v), want (%q, %v)", tt.key, v, ok, tt.value, tt.ok)
		}
	}
}

func TestConditionContext_GetEpochTime(t *testing.T) {
	now := time.Date(2026, 7, 12, 0, 0, 0, 0, time.UTC)
	expected := strconv.FormatInt(now.Unix(), 10)
	ctx := ConditionContext{
		CurrentTime: now,
	}
	v, ok := ctx.Get("aws:EpochTime")
	if !ok {
		t.Fatal("expected EpochTime to be available")
	}
	if v != expected {
		t.Errorf("EpochTime = %s, want %s", v, expected)
	}
}

func TestConditionContext_Extra(t *testing.T) {
	ctx := ConditionContext{}
	ctx.SetExtra("s3:prefix", "documents/")
	v, ok := ctx.Get("s3:prefix")
	if !ok {
		t.Fatal("expected extra key to be found")
	}
	if v != "documents/" {
		t.Errorf("extra value = %q, want %q", v, "documents/")
	}
}

func TestCondition_BinaryEquals(t *testing.T) {
	cb, err := ParseConditionBlock("BinaryEquals", map[string][]string{
		"key": {"base64data=="},
	})
	if err != nil {
		t.Fatalf("ParseConditionBlock: %v", err)
	}
	fn, err := cb.Compile()
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !fn(ConditionContext{extra: map[string]string{"key": "base64data=="}}) {
		t.Error("expected base64 match")
	}
	if fn(ConditionContext{extra: map[string]string{"key": "other=="}}) {
		t.Error("expected different base64 to fail")
	}
}

func TestCondition_NilBlock(t *testing.T) {
	fn, err := (*ConditionBlock)(nil).Compile()
	if err != nil {
		t.Fatalf("Compile nil block: %v", err)
	}
	if !fn(ConditionContext{}) {
		t.Error("expected nil block to always pass")
	}
}
