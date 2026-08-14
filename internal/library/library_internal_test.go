package library

import "testing"

// What counts as "a file on this machine" decides two things at once: whether the daemon reads the
// catalog off disk, and whether it demands a signature on a plugin. Getting it wrong in either
// direction is bad — a remote catalog treated as local would drop the signature requirement.
func TestLocalPath(t *testing.T) {
	for name, tc := range map[string]struct {
		raw       string
		wantPath  string
		wantLocal bool
	}{
		"a relative path":          {"./my-catalog.json", "./my-catalog.json", true},
		"an absolute path":         {"/srv/nocturn/catalog.json", "/srv/nocturn/catalog.json", true},
		"a bare name":              {"catalog.json", "catalog.json", true},
		"a file URL":               {"file:///srv/catalog.json", "/srv/catalog.json", true},
		"a file URL without slash": {"file:/srv/catalog.json", "/srv/catalog.json", true},
		// A drive letter parses as a one-character scheme, and refusing it as "not https" would hit
		// the one platform where this is how a path is written.
		"a windows path": {`C:\nocturn\catalog.json`, `C:\nocturn\catalog.json`, true},
		"https":          {"https://example.com/catalog.json", "", false},
		"plain http":     {"http://example.com/catalog.json", "", false},
		"empty":          {"", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			path, local := localPath(tc.raw)
			if local != tc.wantLocal || path != tc.wantPath {
				t.Errorf("localPath(%q) = %q, %v; want %q, %v", tc.raw, path, local, tc.wantPath, tc.wantLocal)
			}
		})
	}
}

// The signature rule follows the SOURCE, and these are the two ends of it: a remote host must sign
// its plugins, a file on this machine need not — there is no channel to authenticate.
func TestStore_SignaturePolicyFollowsTheSource(t *testing.T) {
	for name, tc := range map[string]struct {
		url  string
		want signaturePolicy
	}{
		"a remote catalog":      {"https://example.com/catalog.json", signaturesRequired},
		"a file":                {"./catalog.json", signaturesOptional},
		"a file URL":            {"file:///srv/catalog.json", signaturesOptional},
		"a server on loopback":  {"http://127.0.0.1:9000/catalog.json", signaturesOptional},
		"localhost by name":     {"http://localhost:9000/catalog.json", signaturesOptional},
		"a host that is remote": {"http://catalog.example/catalog.json", signaturesRequired},
	} {
		t.Run(name, func(t *testing.T) {
			s := New(Source{URL: tc.url}, t.TempDir(), nil)
			if got := s.signaturePolicy(); got != tc.want {
				t.Errorf("signaturePolicy() for %q = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
