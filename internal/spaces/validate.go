package spaces

import (
	"fmt"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
)

func invalid(field string) error { return fmt.Errorf("%w: %s", ErrInvalidArgument, field) }

// ValidateIdentifier never trims or case-folds opaque identifiers.
func ValidateIdentifier(value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) {
		return invalid("identifier")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return invalid("identifier control character")
		}
	}
	return nil
}

func ValidateTenant(tenant string) error {
	if tenant == "*" {
		return invalid("tenant wildcard")
	}
	return ValidateIdentifier(tenant)
}

func ValidateID(id string) error {
	u, err := uuid.Parse(id)
	if err != nil || u == uuid.Nil || u.String() != id {
		return invalid("canonical nonzero UUID")
	}
	return nil
}

func ValidateSubject(subject SubjectRef) error {
	if subject.Kind != User && subject.Kind != Service {
		return invalid("subject kind")
	}
	if err := ValidateIdentifier(subject.Source); err != nil {
		return err
	}
	return ValidateIdentifier(subject.ID)
}

func ValidateSpaceRef(tenant, id string) error {
	if err := ValidateTenant(tenant); err != nil {
		return err
	}
	return ValidateID(id)
}

func ValidateName(name string) error {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 200 {
		return invalid("name")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return invalid("name control character")
		}
	}
	return nil
}

func validateCommand(tenant, id, command string, actor SubjectRef, reason, request string) error {
	if err := ValidateSpaceRef(tenant, id); err != nil {
		return err
	}
	if err := ValidateID(command); err != nil {
		return err
	}
	if err := ValidateSubject(actor); err != nil {
		return err
	}
	if !utf8.ValidString(reason) || !utf8.ValidString(request) {
		return invalid("history text")
	}
	return nil
}

func ValidateCreate(c CreateCommand) error {
	if err := validateCommand(c.TenantID, c.ID, c.CommandID, c.Actor, c.Reason, c.RequestID); err != nil {
		return err
	}
	if c.Kind != Personal && c.Kind != Team {
		return invalid("space kind")
	}
	if c.Actor.Kind != User {
		return invalid("initial user admin")
	}
	return ValidateName(c.Name)
}

func ValidateMemberCommand(c MemberCommand) error {
	if err := validateCommand(c.TenantID, c.SpaceID, c.CommandID, c.Actor, c.Reason, c.RequestID); err != nil {
		return err
	}
	if err := ValidateSubject(c.Target); err != nil {
		return err
	}
	if c.ExpectedRevision < 1 || c.ExpectedRevision == math.MaxInt64 {
		return invalid("expected revision")
	}
	return validateMemberAction(c.Action, c.Role)
}

func validateMemberAction(action MemberAction, role Role) error {
	switch action {
	case RevokeMember:
		if role != "" {
			return invalid("revoke role must be empty")
		}
	case AddMember, ChangeRole, ReactivateMember:
		if role != Viewer && role != Editor && role != Admin {
			return invalid("member role")
		}
	default:
		return invalid("member action")
	}
	return nil
}

func validateLimit(limit int) error {
	if limit < 1 || limit > 1000 {
		return invalid("page limit")
	}
	return nil
}

func ValidateMemberPage(page MemberPage) error {
	if err := validateLimit(page.Limit); err != nil {
		return err
	}
	if page.After != nil {
		return ValidateSubject(*page.After)
	}
	return nil
}

func ValidateSpacePage(page SpacePage) error {
	if err := validateLimit(page.Limit); err != nil {
		return err
	}
	if page.AfterID != "" {
		return ValidateID(page.AfterID)
	}
	return nil
}

func ValidateHistoryPage(page HistoryPage) error {
	if page.AfterRevision < 0 {
		return invalid("history cursor")
	}
	return validateLimit(page.Limit)
}
