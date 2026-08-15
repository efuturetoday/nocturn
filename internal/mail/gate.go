// Package mail is the household's mailbox: reading it, and sending from it.
//
// The two halves are deliberately unequal. Reading is ungated — context, never authority, on the same
// reading as memory_read and knowledge_search — while sending gates on SendKind with the RECIPIENT as
// the target. See ADR-17 for why that is its own kind rather than a net action.
package mail

import (
	"strings"

	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// SendKind is the gate Kind a message leaving the household is checked against. Its Target is the
// recipient address — one Check per recipient, because "send to these three?" is not a question a
// person can answer on a phone.
//
// It is not tools.NetKind, whose Target is a host. The host of an SMTP submission is the household's
// own provider, so a remembered yes for it would stand for every future message to everyone; the
// recipient is what the human is actually deciding about.
const SendKind = "mail.send"

// AddressMatch reports whether a granted pattern covers a recipient address: "*" any, an exact
// address, or a "*@domain" wildcard over one domain. It is the gate.Matcher for SendKind targets.
//
// Comparison folds case, including the local part. That is not what RFC 5321 says — a local part is
// the destination host's business and may be case-sensitive — but a grant is a sentence a human
// typed, and someone who allowed "Chef@firma.de" has not made a statement about "chef@firma.de".
// Erring the other way would ask them again for an address they believe they answered for.
//
// An empty address matches nothing, and so does an empty pattern: a permission must never follow from
// something being absent.
func AddressMatch(pattern, addr string) bool {
	if pattern == "" || addr == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if domain, ok := strings.CutPrefix(pattern, "*@"); ok {
		// Split on "@" rather than testing a suffix: a suffix check would let "*@firma.de" cover
		// "chef@evilfirma.de", which is the same trick "."+base guards against on the net axis.
		return domain != "" && strings.EqualFold(domainOf(addr), domain)
	}
	return strings.EqualFold(pattern, addr)
}

// SendSuggestions offers the human one widening beyond the single address: everyone at that domain.
// "chef@firma.de" -> a "*@firma.de" grant. No suggestion for an address without a domain, and never a
// bare "*" — allowing every recipient is something a person has to write out.
func SendSuggestions(addr string) []gate.Grant {
	if d := domainOf(addr); d != "" {
		return []gate.Grant{{Kind: SendKind, Target: "*@" + d}}
	}
	return nil
}

// domainOf returns the part after the last "@", or "" when there is none. The LAST one, because a
// quoted local part may legally contain an "@" while the domain never does.
func domainOf(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return ""
}
