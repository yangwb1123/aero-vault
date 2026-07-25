package auth

import (
	"testing"
)

func TestParseARN_Full(t *testing.T) {
	arn, err := ParseARN("arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo")
	if err != nil {
		t.Fatalf("ParseARN: %v", err)
	}
	if arn.Partition != "aero" {
		t.Errorf("Partition = %q, want %q", arn.Partition, "aero")
	}
	if arn.Service != "bucket" {
		t.Errorf("Service = %q, want %q", arn.Service, "bucket")
	}
	if arn.Region != "us-east-1" {
		t.Errorf("Region = %q, want %q", arn.Region, "us-east-1")
	}
	if arn.Account != "acme" {
		t.Errorf("Account = %q, want %q", arn.Account, "acme")
	}
	if arn.Resource != "my-bucket/keys/foo" {
		t.Errorf("Resource = %q, want %q", arn.Resource, "my-bucket/keys/foo")
	}
}

func TestParseARN_AWSFormat(t *testing.T) {
	arn, err := ParseARN("arn:aws:s3:::my-bucket")
	if err != nil {
		t.Fatalf("ParseARN: %v", err)
	}
	if arn.Partition != "aws" {
		t.Errorf("Partition = %q", arn.Partition)
	}
	if arn.Service != "s3" {
		t.Errorf("Service = %q", arn.Service)
	}
	if arn.Region != "" {
		t.Errorf("Region = %q", arn.Region)
	}
	if arn.Account != "" {
		t.Errorf("Account = %q", arn.Account)
	}
	if arn.Resource != "my-bucket" {
		t.Errorf("Resource = %q", arn.Resource)
	}
}

func TestParseARN_TooShort(t *testing.T) {
	_, err := ParseARN("arn:aero:short")
	if err == nil {
		t.Fatal("expected error for too-short ARN")
	}
}

func TestParseARN_NoArPrefix(t *testing.T) {
	_, err := ParseARN("aero:bucket:region:acme:resource")
	if err == nil {
		t.Fatal("expected error for missing 'arn' prefix")
	}
}

func TestParseARN_MultiColonResource(t *testing.T) {
	// Resource may contain colons (e.g., "arn:aws:iam::123456:role/my-role").
	arn, err := ParseARN("arn:aws:iam::123456:role/my-role")
	if err != nil {
		t.Fatalf("ParseARN: %v", err)
	}
	if arn.Resource != "role/my-role" {
		t.Errorf("Resource = %q", arn.Resource)
	}
}

func TestMatchARN_ExactMatch(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo",
		"arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo",
	) {
		t.Error("expected exact match")
	}
}

func TestMatchARN_NoMatch(t *testing.T) {
	if MatchARN(
		"arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo",
		"arn:aero:bucket:us-east-1:acme:other-bucket/keys/foo",
	) {
		t.Error("expected no match for different resource")
	}
}

func TestMatchARN_WildcardResource(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:*:acme:my-bucket/*",
		"arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo",
	) {
		t.Error("expected wildcard resource match")
	}
}

func TestMatchARN_WildcardAccount(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:us-east-1:*:my-bucket/keys/foo",
		"arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo",
	) {
		t.Error("expected wildcard account match")
	}
}

func TestMatchARN_WildcardRegion(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:*:acme:my-bucket/keys/foo",
		"arn:aero:bucket:eu-west-1:acme:my-bucket/keys/foo",
	) {
		t.Error("expected wildcard region match")
	}
}

func TestMatchARN_WildcardAll(t *testing.T) {
	if !MatchARN(
		"arn:aero:*:*:*:*",
		"arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo",
	) {
		t.Error("expected wildcard all match")
	}
}

func TestMatchARN_StarOnlyResource(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:*:acme:*",
		"arn:aero:bucket:us-east-1:acme:anything/at/all",
	) {
		t.Error("expected * resource to match anything")
	}
}

func TestMatchARN_QuestionMark(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:*:acme:my-bucket/????",
		"arn:aero:bucket:us-east-1:acme:my-bucket/test",
	) {
		t.Error("expected ? to match single char")
	}
}

func TestMatchARN_QuestionMark_Multiple(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:*:acme:my-bucket/file-??",
		"arn:aero:bucket:us-east-1:acme:my-bucket/file-01",
	) {
		t.Error("expected multiple ? to match")
	}
	if MatchARN(
		"arn:aero:bucket:*:acme:my-bucket/file-??",
		"arn:aero:bucket:us-east-1:acme:my-bucket/file-012",
	) {
		t.Error("expected ? to not match extra chars")
	}
}

func TestMatchARN_WildcardPrefix(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:*:acme:my-bucket/*.txt",
		"arn:aero:bucket:us-east-1:acme:my-bucket/readme.txt",
	) {
		t.Error("expected *.txt match")
	}
	if MatchARN(
		"arn:aero:bucket:*:acme:my-bucket/*.txt",
		"arn:aero:bucket:us-east-1:acme:my-bucket/readme.md",
	) {
		t.Error("expected *.txt NOT to match .md")
	}
}

func TestMatchARN_WildcardInMiddle(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:*:acme:my-bucket/images/*/thumb.jpg",
		"arn:aero:bucket:us-east-1:acme:my-bucket/images/2026/thumb.jpg",
	) {
		t.Error("expected wildcard in middle to match")
	}
}

func TestMatchARN_InvalidPattern(t *testing.T) {
	if MatchARN("invalid", "arn:aero:bucket:r:a:r") {
		t.Error("expected invalid pattern to return false")
	}
}

func TestMatchARN_InvalidARN(t *testing.T) {
	if MatchARN("arn:aero:bucket:*:a:*", "invalid") {
		t.Error("expected invalid ARN to return false")
	}
}

func TestMatchARN_DifferentPartition(t *testing.T) {
	if MatchARN(
		"arn:aws:s3:::my-bucket/*",
		"arn:aero:bucket:us-east-1:acme:my-bucket/key",
	) {
		t.Error("expected different partition to not match")
	}
}

func TestMatchARNList_Exact(t *testing.T) {
	patterns := []string{
		"arn:aero:bucket:*:acme:my-bucket/*",
		"arn:aero:bucket:*:acme:other-bucket/*",
	}
	if !MatchARNList(patterns, "arn:aero:bucket:us-east-1:acme:my-bucket/keys/foo") {
		t.Error("expected match in list")
	}
	if !MatchARNList(patterns, "arn:aero:bucket:us-east-1:acme:other-bucket/keys/bar") {
		t.Error("expected match in list for second pattern")
	}
	if MatchARNList(patterns, "arn:aero:bucket:us-east-1:other-tenant:my-bucket/key") {
		t.Error("expected no match for different tenant")
	}
}

func TestMatchARNList_Empty(t *testing.T) {
	if MatchARNList(nil, "arn:aero:bucket:r:a:r") {
		t.Error("expected empty list to match nothing")
	}
	if MatchARNList([]string{}, "arn:aero:bucket:r:a:r") {
		t.Error("expected empty list to match nothing")
	}
}

func TestNormalizeARN_AlreadyARN(t *testing.T) {
	result := NormalizeARN("arn:aero:bucket:r:a:resource")
	if result != "arn:aero:bucket:r:a:resource" {
		t.Errorf("NormalizeARN = %q", result)
	}
}

func TestNormalizeARN_SimplePath(t *testing.T) {
	result := NormalizeARN("acme/my-bucket/key")
	expected := "arn:aero:bucket:*:acme:my-bucket/key"
	if result != expected {
		t.Errorf("NormalizeARN = %q, want %q", result, expected)
	}
}

func TestNormalizeARN_NoSlash(t *testing.T) {
	result := NormalizeARN("my-bucket")
	expected := "arn:aero:bucket:*:default:my-bucket"
	if result != expected {
		t.Errorf("NormalizeARN = %q, want %q", result, expected)
	}
}

func TestResourceARNFromParts(t *testing.T) {
	arn := ResourceARNFromParts("acme", "my-bucket", "keys/foo")
	expected := "arn:aero:bucket:*:acme:my-bucket/keys/foo"
	if arn != expected {
		t.Errorf("ResourceARNFromParts = %q, want %q", arn, expected)
	}
}

func TestResourceARNFromParts_NoKey(t *testing.T) {
	arn := ResourceARNFromParts("acme", "my-bucket", "")
	expected := "arn:aero:bucket:*:acme:my-bucket"
	if arn != expected {
		t.Errorf("ResourceARNFromParts = %q, want %q", arn, expected)
	}
}

func TestIsARN(t *testing.T) {
	if !IsARN("arn:aero:bucket:r:a:r") {
		t.Error("expected arn: prefix to be detected")
	}
	if IsARN("not-an-arn") {
		t.Error("expected non-arn to return false")
	}
	if IsARN("") {
		t.Error("expected empty string to return false")
	}
}

func TestMatchARN_DeepWildcard(t *testing.T) {
	// Match any object under any bucket for a given tenant.
	if !MatchARN(
		"arn:aero:bucket:*:acme:*",
		"arn:aero:bucket:us-east-1:acme:my-bucket/a/b/c/d/file.txt",
	) {
		t.Error("expected deep wildcard to match entire path")
	}
}

func TestMatchARN_EmptyResource(t *testing.T) {
	if !MatchARN(
		"arn:aero:bucket:*:acme:",
		"arn:aero:bucket:us-east-1:acme:",
	) {
		t.Error("expected empty resource to match")
	}
}

func TestMatchARN_DifferentService(t *testing.T) {
	if MatchARN(
		"arn:aero:bucket:*:*:*",
		"arn:aero:object:*:acme:my-bucket/key",
	) {
		t.Error("expected different service to not match")
	}
}
