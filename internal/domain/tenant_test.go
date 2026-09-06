package domain

import (
	"slices"
	"testing"
)

// TestParseScopesAcceptsBothSeparators pins the bug that made a fully
// authorized account look unauthorized: LinkedIn's authorization dialog takes
// spaces, its introspection endpoint answers with commas.
func TestParseScopesAcceptsBothSeparators(t *testing.T) {
	want := []string{"openid", "profile", "email", "w_member_social"}
	for _, raw := range []string{
		"openid profile email w_member_social",
		"openid,profile,email,w_member_social",
		"openid, profile,  email\tw_member_social\n",
	} {
		if got := ParseScopes(raw); !slices.Equal(got, want) {
			t.Errorf("ParseScopes(%q) = %v", raw, got)
		}
	}
	if got := ParseScopes("   "); len(got) != 0 {
		t.Errorf("ParseScopes(vide) = %v", got)
	}
}

func TestHasScopeAfterParsing(t *testing.T) {
	tenant := &Tenant{Scopes: ParseScopes("email,openid,profile,w_member_social")}
	if !tenant.HasScope("w_member_social") {
		t.Fatalf("permission accordée vue comme absente: %v", tenant.Scopes)
	}
	if tenant.HasScope("r_member_social") {
		t.Fatal("permission absente vue comme accordée")
	}
}
