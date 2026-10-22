package access

import (
	"errors"
	"reflect"
	"testing"
)

func TestValidateChunkRefPreservesExactIdentity(t *testing.T) {
	refs := []ChunkRef{
		{TenantID: "default", Bucket: "default", Key: "a.txt", ObjectID: 1},
		{TenantID: "Team-A", Bucket: "Docs", Key: " Reports/Résumé.txt ", ObjectID: 42},
		{TenantID: "other", Bucket: "archive", Key: "literal@vname", ObjectID: 1<<63 - 1},
	}
	for _, ref := range refs {
		t.Run(ref.Key, func(t *testing.T) {
			before := ref
			if err := ValidateChunkRef(ref); err != nil {
				t.Fatalf("ValidateChunkRef(%+v): %v", ref, err)
			}
			if ref != before {
				t.Fatalf("identity changed: got %+v, want %+v", ref, before)
			}
		})
	}
}

func TestValidateChunkRefRejectsIncompleteIdentity(t *testing.T) {
	tests := []struct {
		name string
		ref  ChunkRef
	}{
		{"zero_value", ChunkRef{}},
		{"missing_tenant", ChunkRef{Bucket: "docs", Key: "a.txt", ObjectID: 1}},
		{"missing_bucket", ChunkRef{TenantID: "alpha", Key: "a.txt", ObjectID: 1}},
		{"missing_key", ChunkRef{TenantID: "alpha", Bucket: "docs", ObjectID: 1}},
		{"zero_id", ChunkRef{TenantID: "alpha", Bucket: "docs", Key: "a.txt"}},
		{"negative_id", ChunkRef{TenantID: "alpha", Bucket: "docs", Key: "a.txt", ObjectID: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := tt.ref
			if err := ValidateChunkRef(tt.ref); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("ValidateChunkRef(%+v) = %v, want ErrInvalidArgument", tt.ref, err)
			}
			if tt.ref != before {
				t.Fatalf("invalid identity changed: got %+v, want %+v", tt.ref, before)
			}
		})
	}
}

func TestChunkRefIdentityFields(t *testing.T) {
	want := []struct {
		name string
		kind reflect.Kind
	}{
		{"TenantID", reflect.String},
		{"Bucket", reflect.String},
		{"Key", reflect.String},
		{"ObjectID", reflect.Int64},
	}
	typ := reflect.TypeFor[ChunkRef]()
	if typ.NumField() != len(want) {
		t.Fatalf("ChunkRef has %d fields, want exactly %d", typ.NumField(), len(want))
	}
	for i, expected := range want {
		field := typ.Field(i)
		if field.Name != expected.name || field.Type.Kind() != expected.kind || !field.IsExported() {
			t.Errorf("field %d = %v, want exported %s (%s)", i, field, expected.name, expected.kind)
		}
	}
}
