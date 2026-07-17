package netcap

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/efuturetoday/nocturn/internal/capability"
	"github.com/efuturetoday/nocturn/internal/gateway"
)

// resolver returns the configured resolver, defaulting to the system one.
func (n *Net) resolver() *net.Resolver {
	if n.Resolver != nil {
		return n.Resolver
	}
	return net.DefaultResolver
}

// Lookup resolves host for the given DNS record type and returns the records as
// strings. Supported types: A (IPv4), AAAA (IPv6), IP (both), MX, TXT, CNAME, NS,
// PTR (reverse — target is an IP), SRV; an empty type defaults to A.
//
// Like Fetch it is gated on the host — DNS is an exfiltration channel whatever the
// record type (a lookup to an attacker's nameserver leaks whatever is encoded in
// the query), so an unknown host escalates to approval. The record TYPE is not an
// authority axis: the reach that matters is the queried name, identical across
// types, so it is a plain effect parameter and never part of the gated Call.
func (n *Net) Lookup(ctx context.Context, host, recordType string) ([]string, error) {
	typ := normalizeRecordType(recordType)
	call := capability.Call{Family: "dns", Write: false, Target: host}
	intent := "resolve " + typ + " " + host
	// Egress: the queried NAME is the exfiltration surface (a lookup to an attacker's
	// nameserver leaks whatever is encoded in it). Ingress: records — TXT above all —
	// are attacker-controllable inbound text, so redact any echoed vault secret before
	// it reaches the model.
	return gateway.Do(ctx, n.Guard, call, intent,
		gateway.ScanEgress(n.Scanner, func() []string { return []string{host} }),
		func() ([]string, error) {
			records, err := n.lookupRecords(ctx, typ, host, recordType)
			if err != nil {
				return nil, err
			}
			for i := range records {
				records[i] = string(n.Scanner.RedactIngress([]byte(records[i])))
			}
			return records, nil
		})
}

// lookupRecords performs the raw resolution for a record type (no gating/scanning).
func (n *Net) lookupRecords(ctx context.Context, typ, host, recordType string) ([]string, error) {
	r := n.resolver()
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
		return nil, fmt.Errorf("netcap: unsupported DNS record type %q", recordType)
	}
}

// Resolve is the address lookup (A + AAAA), the common convenience over Lookup.
func (n *Net) Resolve(ctx context.Context, host string) ([]string, error) {
	return n.Lookup(ctx, host, "IP")
}

// normalizeRecordType upper-cases and trims a record type, defaulting empty to A.
func normalizeRecordType(t string) string {
	if t = strings.ToUpper(strings.TrimSpace(t)); t != "" {
		return t
	}
	return "A"
}
