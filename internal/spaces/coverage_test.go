package spaces

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

const coverageTestID = "12345678-1234-1234-1234-123456789abc"

func coverageTestWitness(tenant, bucket string) BindingResult {
	return BindingResult{TenantID: tenant, Bucket: bucket, TenantKnown: true,
		TenantStatus: "active", State: BindingBound, SpaceID: coverageTestID, Revision: 1}
}

func coverageTestResult() CoverageResult {
	return CoverageResult{Scope: CoverageScope{Kind: CoverageTenant, TenantID: "Alpha"},
		TenantKnown: true, TenantStatus: "active", State: CoverageBound, Complete: true,
		Witnesses: []BindingResult{coverageTestWitness("Alpha", "Bucket")}}
}

func TestCoverageScopeValidation(t *testing.T) {
	for _, scope := range []CoverageScope{
		{Kind: CoverageGlobal}, {Kind: CoverageTenant, TenantID: "Alpha"},
		{Kind: CoverageTenant, TenantID: "legacy'--/桶"},
	} {
		if err := ValidateCoverageScope(scope); err != nil {
			t.Errorf("literal scope %+v: %v", scope, err)
		}
	}
	bad := []CoverageScope{{}, {Kind: "other"}, {Kind: CoverageGlobal, TenantID: "Alpha"},
		{Kind: CoverageGlobal, TenantID: "*"}}
	for _, tenant := range []string{"", "*", " Alpha", "Alpha ", "\xff", "a\x00b", "a\nb", "a\tb"} {
		bad = append(bad, CoverageScope{Kind: CoverageTenant, TenantID: tenant})
	}
	for _, scope := range bad {
		if err := ValidateCoverageScope(scope); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("invalid scope %+v: %v", scope, err)
		}
	}
}

func TestCoverageCompleteAndUnknownValues(t *testing.T) {
	global := CoverageScope{Kind: CoverageGlobal}
	tenant := CoverageScope{Kind: CoverageTenant, TenantID: "Alpha"}
	values := []CoverageResult{
		{Scope: global, State: CoverageUnbound, Complete: true},
		{Scope: tenant, State: CoverageUnbound, Complete: true},
		{Scope: global, State: CoverageUnknown}, {Scope: tenant, State: CoverageUnknown},
	}
	for _, status := range []string{"active", "disabled", "Suspended", "", " active "} {
		bound := coverageTestResult()
		bound.TenantStatus, bound.Witnesses[0].TenantStatus = status, status
		bound.Witnesses[0].Revision = math.MaxInt64
		values = append(values, bound)
		bound.Complete = false
		values = append(values, bound)
		values = append(values, CoverageResult{Scope: tenant, TenantKnown: true,
			TenantStatus: status, State: CoverageUnbound, Complete: true})
		values = append(values, CoverageResult{Scope: tenant, TenantKnown: true,
			TenantStatus: status, State: CoverageUnknown})
	}
	for i, value := range values {
		if err := ValidateCoverageResult(value); err != nil {
			t.Errorf("valid fact %d %+v: %v", i, value, err)
		}
	}
}

func TestCoverageRejectsIncompleteAbsenceAndInvalidStates(t *testing.T) {
	for name, mutate := range map[string]func(*CoverageResult){
		"zero scope":          func(v *CoverageResult) { v.Scope = CoverageScope{} },
		"zero state":          func(v *CoverageResult) { v.State = "" },
		"unknown state":       func(v *CoverageResult) { v.State = "future" },
		"fault state":         func(v *CoverageResult) { v.State = "fault" },
		"missing witness":     func(v *CoverageResult) { v.Witnesses = nil },
		"unbound witness":     func(v *CoverageResult) { v.State = CoverageUnbound },
		"unknown witness":     func(v *CoverageResult) { v.State = CoverageUnknown; v.Complete = false },
		"unknown complete":    func(v *CoverageResult) { v.State = CoverageUnknown; v.Witnesses = nil },
		"incomplete absence":  func(v *CoverageResult) { v.State = CoverageUnbound; v.Witnesses = nil; v.Complete = false },
		"unknown with status": func(v *CoverageResult) { v.TenantKnown = false },
		"unknown with bound":  func(v *CoverageResult) { v.TenantKnown = false; v.TenantStatus = "" },
		"global known":        func(v *CoverageResult) { v.Scope = CoverageScope{Kind: CoverageGlobal}; v.TenantStatus = "" },
		"global status":       func(v *CoverageResult) { v.Scope = CoverageScope{Kind: CoverageGlobal}; v.TenantKnown = false },
	} {
		t.Run(name, func(t *testing.T) {
			value := coverageTestResult()
			mutate(&value)
			if err := ValidateCoverageResult(value); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("malformed fact accepted: %+v, %v", value, err)
			}
		})
	}
	if err := ValidateCoverageResult(CoverageResult{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero result accepted: %v", err)
	}
}

func TestCoverageWitnessValidation(t *testing.T) {
	mutations := map[string]func(*BindingResult){
		"other tenant":   func(v *BindingResult) { v.TenantID = "alpha" },
		"empty tenant":   func(v *BindingResult) { v.TenantID = "" },
		"empty bucket":   func(v *BindingResult) { v.Bucket = "" },
		"wildcard":       func(v *BindingResult) { v.Bucket = "*" },
		"bad bucket":     func(v *BindingResult) { v.Bucket = " Bucket" },
		"bad UTF8":       func(v *BindingResult) { v.Bucket = "\xff" },
		"control":        func(v *BindingResult) { v.Bucket = "a\x00b" },
		"unknown tenant": func(v *BindingResult) { v.TenantKnown = false; v.TenantStatus = "" },
		"raw status":     func(v *BindingResult) { v.TenantStatus = "disabled" },
		"zero state":     func(v *BindingResult) { v.State = "" },
		"unknown state":  func(v *BindingResult) { v.State = "future" },
		"unbound":        func(v *BindingResult) { v.State = BindingUnbound; v.SpaceID = ""; v.Revision = 0 },
		"empty ID":       func(v *BindingResult) { v.SpaceID = "" },
		"zero UUID":      func(v *BindingResult) { v.SpaceID = "00000000-0000-0000-0000-000000000000" },
		"uppercase UUID": func(v *BindingResult) { v.SpaceID = strings.ToUpper(coverageTestID) },
		"zero revision":  func(v *BindingResult) { v.Revision = 0 },
		"negative":       func(v *BindingResult) { v.Revision = -1 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			value := coverageTestResult()
			mutate(&value.Witnesses[0])
			if err := ValidateCoverageResult(value); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("invalid witness %+v: %v", value.Witnesses[0], err)
			}
		})
	}
}

func TestCoverageGlobalWitnessConsistency(t *testing.T) {
	value := CoverageResult{Scope: CoverageScope{Kind: CoverageGlobal}, State: CoverageBound,
		Complete: true, Witnesses: []BindingResult{coverageTestWitness("Alpha", "Bucket"),
			coverageTestWitness("Beta", "Bucket")}}
	if err := ValidateCoverageResult(value); err != nil {
		t.Fatalf("same bucket and UUID in distinct tenants: %v", err)
	}
	for _, kind := range []string{"duplicate bucket", "duplicate space", "contradictory status"} {
		t.Run(kind, func(t *testing.T) {
			v := value
			v.Witnesses = append([]BindingResult(nil), value.Witnesses...)
			v.Witnesses[1].TenantID = "Alpha"
			switch kind {
			case "duplicate bucket":
				v.Witnesses[1].SpaceID = "22345678-1234-1234-1234-123456789abc"
			case "duplicate space":
				v.Witnesses[1].Bucket = "Other"
			case "contradictory status":
				v.Witnesses[1].Bucket = "Other"
				v.Witnesses[1].SpaceID = "22345678-1234-1234-1234-123456789abc"
				v.Witnesses[1].TenantStatus = "disabled"
			}
			if err := ValidateCoverageResult(v); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("inconsistent witnesses accepted: %+v: %v", v, err)
			}
		})
	}
}

func TestCoverageScopeEchoAndNoMutation(t *testing.T) {
	value := coverageTestResult()
	before := value
	before.Witnesses = append([]BindingResult(nil), value.Witnesses...)
	if err := ValidateCoverageObservation(value.Scope, value); err != nil {
		t.Fatal(err)
	}
	for _, requested := range []CoverageScope{
		{}, {Kind: CoverageGlobal}, {Kind: CoverageTenant, TenantID: "alpha"},
		{Kind: CoverageTenant, TenantID: "Other"}, {Kind: CoverageTenant, TenantID: " Alpha"},
	} {
		if err := ValidateCoverageObservation(requested, value); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("bad echo %+v: %v", requested, err)
		}
	}
	if err := ValidateCoverageObservation(value.Scope, CoverageResult{}); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero observation accepted: %v", err)
	}
	if !reflect.DeepEqual(value, before) {
		t.Fatal("coverage validation mutated facts")
	}
}

func TestCoverageFactsHaveNoPermissionFields(t *testing.T) {
	fields := []string{"Scope", "TenantKnown", "TenantStatus", "State", "Complete", "Witnesses"}
	typ := reflect.TypeFor[CoverageResult]()
	if typ.NumField() != len(fields) {
		t.Fatal("coverage facts must not acquire permission or principal fields")
	}
	for i, name := range fields {
		if typ.Field(i).Name != name {
			t.Errorf("field %d = %s, want %s", i, typ.Field(i).Name, name)
		}
	}
	if reflect.TypeFor[CoverageReader]().NumMethod() != 1 {
		t.Fatal("coverage must remain an independent narrow read port")
	}
}
