package spaces

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

const bindingTestID = "12345678-1234-1234-1234-123456789abc"

func TestBindingInputValidation(t *testing.T) {
	for _, pair := range [][2]string{{"alpha", "default"}, {"Alpha", "a b/桶"}, {"default", "legacy'--"}} {
		if err := ValidateBindingInput(pair[0], pair[1]); err != nil {
			t.Errorf("literal input %q: %v", pair, err)
		}
	}
	for _, bad := range []string{"", "*", " alpha", "alpha ", "\xff", "a\x00b", "a\nb", "a\tb"} {
		for _, pair := range [][2]string{{bad, "bucket"}, {"alpha", bad}} {
			if err := ValidateBindingInput(pair[0], pair[1]); !errors.Is(err, ErrInvalidArgument) {
				t.Errorf("invalid input %q: %v", pair, err)
			}
		}
	}
}

func TestBindingResultValidation(t *testing.T) {
	bound := BindingResult{TenantID: "Alpha", Bucket: "Bucket", TenantKnown: true,
		TenantStatus: "active", State: BindingBound, SpaceID: bindingTestID, Revision: 1}
	unbound := BindingResult{TenantID: "Alpha", Bucket: "Bucket", State: BindingUnbound}
	for _, status := range []string{"active", "disabled", "Suspended", "", " paused "} {
		value := bound
		value.TenantStatus, value.Revision = status, math.MaxInt64
		if err := ValidateBindingResult(value); err != nil {
			t.Errorf("bound raw status %q: %v", status, err)
		}
		value.State, value.SpaceID, value.Revision = BindingUnbound, "", 0
		if err := ValidateBindingResult(value); err != nil {
			t.Errorf("known unbound raw status %q: %v", status, err)
		}
	}
	if err := ValidateBindingResult(unbound); err != nil {
		t.Fatal(err)
	}
	for i, value := range bindingTestInvalidResults(bound, unbound) {
		if err := ValidateBindingResult(value); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("invalid result %d = %+v: %v", i, value, err)
		}
	}
	fields := []string{"TenantID", "Bucket", "TenantKnown", "TenantStatus", "State", "SpaceID", "Revision"}
	typ := reflect.TypeFor[BindingResult]()
	if typ.NumField() != len(fields) {
		t.Fatal("binding facts must not acquire permission or identity fields")
	}
	for i, name := range fields {
		if typ.Field(i).Name != name {
			t.Errorf("field %d = %s, want %s", i, typ.Field(i).Name, name)
		}
	}
}

func bindingTestInvalidResults(bound, unbound BindingResult) []BindingResult {
	values := []BindingResult{{}}
	for _, mutate := range []func(*BindingResult){
		func(v *BindingResult) { v.State = "" },
		func(v *BindingResult) { v.State = "fault" },
		func(v *BindingResult) { v.State = "other" },
		func(v *BindingResult) { v.TenantKnown, v.TenantStatus = false, "" },
		func(v *BindingResult) { v.SpaceID = "" },
		func(v *BindingResult) { v.SpaceID = "bad-id" },
		func(v *BindingResult) { v.SpaceID = "00000000-0000-0000-0000-000000000000" },
		func(v *BindingResult) { v.SpaceID = strings.ToUpper(bindingTestID) },
		func(v *BindingResult) { v.Revision = 0 },
		func(v *BindingResult) { v.Revision = -1 },
	} {
		value := bound
		mutate(&value)
		values = append(values, value)
	}
	for _, mutate := range []func(*BindingResult){
		func(v *BindingResult) { v.TenantStatus = "active" },
		func(v *BindingResult) { v.SpaceID = bindingTestID },
		func(v *BindingResult) { v.Revision = 1 },
		func(v *BindingResult) { v.Revision = -1 },
	} {
		value := unbound
		mutate(&value)
		values = append(values, value)
	}
	for _, bad := range []string{"", "*", " Alpha", "Alpha ", "\xff", "a\x00b"} {
		value := bound
		value.TenantID = bad
		values = append(values, value)
		value = bound
		value.Bucket = bad
		values = append(values, value)
	}
	return values
}
