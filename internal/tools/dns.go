package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/agentkit/gate"
)

// dns_resolve gates on NetKind with the target host — one host allowlist (and one session grant)
// spans fetch, DNS, and ping alike. DNS is an exfiltration channel whatever the record type (a lookup
// to an attacker's nameserver leaks whatever is encoded in the name), so an unknown host escalates to
// the same human approval as a fetch. It is a read: a lookup observes, it never mutates. The record
// TYPE is not an authority axis — the reach that matters is the queried name — so it is a plain
// parameter, never part of the gated action.

func (n *Net) resolveTool() (agentkit.Tool, error) {
	return agentkit.NewTool("dns_resolve",
		"Resolve a hostname's DNS records. Returns a JSON object {host, type, records}.",
		n.resolve,
		agentkit.WithSchema(agentkit.Object(
			agentkit.Prop("host", agentkit.String("The hostname to resolve (an IP for PTR)")),
			agentkit.Prop("type", agentkit.String("Record type (default A)").
				WithEnum("A", "AAAA", "IP", "MX", "TXT", "CNAME", "NS", "PTR", "SRV")),
		).Require("host")),
	)
}

func (n *Net) resolve(ctx context.Context, args string) (string, error) {
	var a struct {
		Host string `json:"host"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.Host == "" {
		return "", errors.New("missing required field: host")
	}
	typ := normalizeRecordType(a.Type)
	if err := gate.Check(ctx, gate.Action{Kind: NetKind, Target: a.Host}, HostMatch, NetSuggestions(a.Host)...); err != nil {
		return "", err
	}
	// Egress: the queried NAME is the exfiltration surface. Ingress: records (TXT above all) are
	// attacker-controllable inbound text, so redact any echoed vault secret before it reaches the model.
	if n.scanner != nil {
		if err := n.scanner.ScanEgress(a.Host, ""); err != nil {
			return "", fmt.Errorf("egress blocked: %w", err)
		}
	}
	records, err := lookupRecords(ctx, typ, a.Host)
	if err != nil {
		return "", err
	}
	if n.scanner != nil {
		for i := range records {
			records[i] = string(n.scanner.RedactIngress([]byte(records[i])))
		}
	}
	out, err := json.Marshal(struct {
		Host    string   `json:"host"`
		Type    string   `json:"type"`
		Records []string `json:"records"`
	}{a.Host, typ, records})
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// lookupRecords performs the raw resolution for a record type (no gating/scanning).
func lookupRecords(ctx context.Context, typ, host string) ([]string, error) {
	r := net.DefaultResolver
	switch typ {
	case "A", "AAAA", "IP":
		network := map[string]string{"A": "ip4", "AAAA": "ip6", "IP": "ip"}[typ]
		ips, err := r.LookupIP(ctx, network, host)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(ips))
		for i, ip := range ips {
			out[i] = ip.String()
		}
		return out, nil
	case "MX":
		mxs, err := r.LookupMX(ctx, host)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(mxs))
		for i, mx := range mxs {
			out[i] = fmt.Sprintf("%d %s", mx.Pref, mx.Host)
		}
		return out, nil
	case "TXT":
		return r.LookupTXT(ctx, host)
	case "CNAME":
		cname, err := r.LookupCNAME(ctx, host)
		if err != nil {
			return nil, err
		}
		return []string{cname}, nil
	case "NS":
		nss, err := r.LookupNS(ctx, host)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(nss))
		for i, ns := range nss {
			out[i] = ns.Host
		}
		return out, nil
	case "PTR":
		return r.LookupAddr(ctx, host)
	case "SRV":
		// Empty service+proto looks up the name directly (RFC 2782 already-qualified).
		_, srvs, err := r.LookupSRV(ctx, "", "", host)
		if err != nil {
			return nil, err
		}
		out := make([]string, len(srvs))
		for i, s := range srvs {
			out[i] = fmt.Sprintf("%d %d %d %s", s.Priority, s.Weight, s.Port, s.Target)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported DNS record type %q", typ)
	}
}

// normalizeRecordType upper-cases and trims a record type, defaulting empty to A.
func normalizeRecordType(t string) string {
	if t = strings.ToUpper(strings.TrimSpace(t)); t != "" {
		return t
	}
	return "A"
}
