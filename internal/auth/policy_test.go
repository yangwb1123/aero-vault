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
