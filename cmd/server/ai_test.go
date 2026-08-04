package main

import (
	"reflect"
	"testing"

	"github.com/aero-vault/aero-vault/internal/config"
)

func TestAIStartupTenants(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "default", want: []string{"default"}},
		{name: "configured", in: []string{"alpha", "beta"}, want: []string{"alpha", "beta"}},
		{name: "deduplicated", in: []string{"alpha", "", "alpha", "beta"}, want: []string{"alpha", "beta"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Reconcile.Tenants = tt.in
			if got := aiStartupTenants(cfg); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("aiStartupTenants()=%v want %v", got, tt.want)
			}
		})
	}
}
