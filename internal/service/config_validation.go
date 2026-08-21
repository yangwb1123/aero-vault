package service

import (
	"fmt"

	"github.com/aero-vault/aero-vault/internal/repository"
)

func validateQuotaLimits(maxBytes, maxObjects int64) error {
	if maxBytes < 0 {
		return fmt.Errorf("%w: max_bytes must be >= 0", ErrInvalidArgs)
	}
	if maxObjects < 0 {
		return fmt.Errorf("%w: max_objects must be >= 0", ErrInvalidArgs)
	}
	return nil
}

func validateLifecycleDays(name string, days int) error {
	if days < 0 {
		return fmt.Errorf("%w: %s must be >= 0", ErrInvalidArgs, name)
	}
	return nil
}

func validateLifecycleConfig(config repository.LifecycleConfig) error {
	fields := []struct {
		name string
		days int
	}{
		{"days", config.ExpireAfterDays},
		{"noncurrent_days", config.NoncurrentDays},
		{"noncurrent_count", config.NoncurrentCount},
		{"noncurrent_transition_days", config.NoncurrentTransitionDays},
	}
	for _, field := range fields {
		if err := validateLifecycleDays(field.name, field.days); err != nil {
			return err
		}
	}
	for i, rule := range config.TransitionRules {
		if err := validateLifecycleDays(fmt.Sprintf("transition_rules[%d].days", i), rule.Days); err != nil {
			return err
		}
	}
	return nil
}
