package spaces

import "testing"

func TestSubjectRefCompleteIdentity(t *testing.T) {
	a := SubjectRef{Source: "issuer:a", Kind: User, ID: "same"}
	b, c := a, a
	b.Source = "issuer:b"
	c.Kind = Service
	refs := map[SubjectRef]bool{a: true, b: true, c: true}
	if len(refs) != 3 {
		t.Fatal("identity collapsed source or kind")
	}
	for ref := range refs {
		if err := ValidateSubject(ref); err != nil {
			t.Fatal(err)
		}
	}
}
