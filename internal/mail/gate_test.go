package mail_test

import (
	"slices"
	"testing"

	"github.com/efuturetoday/nocturn/agentkit/gate"
	"github.com/efuturetoday/nocturn/internal/mail"
)

func TestAddressMatch(t *testing.T) {
	cases := []struct {
		name    string
		pattern string
		addr    string
		want    bool
	}{
		{"exact", "chef@firma.de", "chef@firma.de", true},
		{"exact folds case", "Chef@Firma.DE", "chef@firma.de", true},
		{"a different local part is a different recipient", "chef@firma.de", "buero@firma.de", false},
		{"a different domain is a different recipient", "chef@firma.de", "chef@firma.com", false},
		{"domain wildcard", "*@firma.de", "chef@firma.de", true},
		{"domain wildcard folds case", "*@FIRMA.de", "chef@firma.de", true},
		{"domain wildcard covers any local part", "*@firma.de", "irgendwer@firma.de", true},
		{"domain wildcard stops at the domain", "*@firma.de", "chef@sub.firma.de", false},
		{"any", "*", "chef@firma.de", true},
		{"empty pattern matches nothing", "", "chef@firma.de", false},
		{"empty address matches nothing", "*@firma.de", "", false},
		{"empty address is not covered by any", "*", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mail.AddressMatch(tc.pattern, tc.addr); got != tc.want {
				t.Errorf("AddressMatch(%q, %q) = %v, want %v", tc.pattern, tc.addr, got, tc.want)
			}
		})
	}
}

// TestAddressMatchRejectsSuffixTrick pins the split-on-"@" guard: a domain wildcard must not cover a
// domain that merely ENDS in the granted one. A suffix check would hand every message meant for the
// office to an attacker who registered evilfirma.de.
func TestAddressMatchRejectsSuffixTrick(t *testing.T) {
	const pattern = "*@firma.de"
	for _, addr := range []string{
		"chef@evilfirma.de",
		"chef@notfirma.de",
		"chef@firma.de.evil.com",
	} {
		if mail.AddressMatch(pattern, addr) {
			t.Errorf("grant %q wrongly covered %q", pattern, addr)
		}
	}
}

// TestAddressMatchIsAGateMatcher pins that the signature stays usable where it is meant to be used.
func TestAddressMatchIsAGateMatcher(t *testing.T) {
	var m gate.Matcher = mail.AddressMatch
	if !m("*@firma.de", "chef@firma.de") {
		t.Fatal("AddressMatch does not behave as the gate.Matcher for send targets")
	}
}

func TestSendSuggestions(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want []gate.Grant
	}{
		{
			name: "an address widens to its domain",
			addr: "chef@firma.de",
			want: []gate.Grant{{Kind: mail.SendKind, Target: "*@firma.de"}},
		},
		{
			name: "a subdomain widens to exactly that subdomain, not the parent",
			addr: "chef@mail.firma.de",
			want: []gate.Grant{{Kind: mail.SendKind, Target: "*@mail.firma.de"}},
		},
		{"no domain, no suggestion", "chef", nil},
		{"a trailing @ is no domain", "chef@", nil},
		{"nothing to widen", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mail.SendSuggestions(tc.addr)
			if !slices.Equal(got, tc.want) {
				t.Errorf("SendSuggestions(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

// TestSendSuggestionsNeverOffersEveryone pins that the widening a human is offered stops at one
// domain. Allowing every recipient in the world is a sentence someone has to write out themselves.
func TestSendSuggestionsNeverOffersEveryone(t *testing.T) {
	for _, addr := range []string{"chef@firma.de", "a@b.c", "x@localhost"} {
		for _, g := range mail.SendSuggestions(addr) {
			if g.Target == "*" {
				t.Errorf("SendSuggestions(%q) offered a bare wildcard", addr)
			}
		}
	}
}

// TestSuggestionsAreCoveredByTheMatcher pins the two halves against each other: a widening offered to
// the human must actually cover the address it was offered for. They are separate functions and a
// changed split rule in one would otherwise leave a grant that never matches — an approval that
// silently does nothing.
func TestSuggestionsAreCoveredByTheMatcher(t *testing.T) {
	for _, addr := range []string{"chef@firma.de", "chef@mail.firma.de", "a@b.co.uk"} {
		for _, g := range mail.SendSuggestions(addr) {
			if !mail.AddressMatch(g.Target, addr) {
				t.Errorf("suggestion %q does not cover %q", g.Target, addr)
			}
		}
	}
}
