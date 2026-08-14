package library

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"strings"
	"testing"
)

// The whole point of signing plugins is that a catalog host cannot substitute code. These cases are
// the substitutions worth naming: no signature, another key's signature, an edited manifest, an
// edited script, an edited bundled skill, and — the one a naive scheme misses — a valid signature
// lifted from a DIFFERENT entry.
func TestVerifySignature(t *testing.T) {
	pub, priv := testKey(t)
	t.Setenv(devKeyEnv, base64.StdEncoding.EncodeToString(pub))

	signed := signedItem(t, priv, "gmail", "MANIFEST", "SCRIPT", "SKILL")
	other := signedItem(t, priv, "calendar", "M2", "S2", "")

	_, notOurs := testKey(t)
	foreign := signedItem(t, notOurs, "gmail", "MANIFEST", "SCRIPT", "SKILL")

	for name, tc := range map[string]struct {
		item PluginItem
		want string // empty = it must verify
	}{
		"a signed entry verifies":        {signed, ""},
		"no signature":                   {tweaked(signed, func(p *PluginItem) { p.Signature = "" }), "unsigned"},
		"not base64":                     {tweaked(signed, func(p *PluginItem) { p.Signature = "!!!" }), "not base64"},
		"another key":                    {foreign, "no trusted key"},
		"the id was changed":             {tweaked(signed, func(p *PluginItem) { p.ID = "gmai1" }), "no trusted key"},
		"the install folder was changed": {tweaked(signed, func(p *PluginItem) { p.Folder = "elsewhere" }), "no trusted key"},
		"the manifest was edited":        {resigned(t, signed, "OTHER MANIFEST", "SCRIPT", "SKILL"), "no trusted key"},
		"the script was edited":          {resigned(t, signed, "MANIFEST", "OTHER SCRIPT", "SKILL"), "no trusted key"},
		"the bundled skill was edited":   {resigned(t, signed, "MANIFEST", "SCRIPT", "OTHER SKILL"), "no trusted key"},
		// The attack a per-file signature would allow: keep one entry's signature and put another
		// entry's artifacts behind it.
		"another entry's signature": {tweaked(signed, func(p *PluginItem) { p.Signature = other.Signature }), "no trusted key"},
	} {
		t.Run(name, func(t *testing.T) {
			err := verifySignature(tc.item, signaturesRequired)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("verifySignature() = %v, want it to verify", err)
			case tc.want != "" && err == nil:
				t.Errorf("verifySignature() accepted the entry; want an error mentioning %q", tc.want)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Errorf("verifySignature() = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A build that trusts no key must say so rather than reporting each entry as badly signed — the two
// look identical from the log and have completely different fixes.
func TestVerifySignature_NoTrustedKeys(t *testing.T) {
	t.Setenv(devKeyEnv, "")
	saved := signingKeys
	signingKeys = nil
	t.Cleanup(func() { signingKeys = saved })

	_, priv := testKey(t)
	err := verifySignature(signedItem(t, priv, "gmail", "M", "S", ""), signaturesRequired)
	if err == nil || !strings.Contains(err.Error(), "trusts no catalog signing key") {
		t.Errorf("verifySignature() = %v, want it to name the missing key", err)
	}
}

// A malformed key is a mistake somebody made, and the message has to say WHICH of the two sources it
// came from: one is a bug in the build, the other a typo in a shell.
func TestTrustedKeys_NamesTheBadSource(t *testing.T) {
	t.Setenv(devKeyEnv, "not-base64!!")
	if _, err := trustedKeys(); err == nil || !strings.Contains(err.Error(), devKeyEnv) {
		t.Errorf("trustedKeys() = %v, want it to name %s", err, devKeyEnv)
	}
}

// The keys this build ships must be usable, or every plugin entry is dropped in the field with an
// error nobody sees until they open the library.
func TestTrustedKeys_TheCompiledInKeysDecode(t *testing.T) {
	t.Setenv(devKeyEnv, "")
	keys, err := trustedKeys()
	if err != nil {
		t.Fatalf("trustedKeys() = %v; a compiled-in key is malformed", err)
	}
	if len(keys) == 0 {
		t.Error("this build trusts no signing key, so no plugin can ever be installed")
	}
}

// A dropped entry has to be named in the log, or "my plugin is not in the library" is unanswerable.
func TestValidPlugins_LogsWhyAnEntryWasDropped(t *testing.T) {
	var log strings.Builder
	logger := slog.New(slog.NewTextHandler(&log, nil))

	kept := validPlugins([]PluginItem{{ID: "gmail", Manifest: "m", Script: "s", Folder: "gmail"}}, logger, signaturesRequired)
	if len(kept) != 0 {
		t.Fatal("an entry with mismatched digests was offered")
	}
	if !strings.Contains(log.String(), "gmail") {
		t.Errorf("the log does not name the dropped entry: %q", log.String())
	}
}

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// signedItem builds an entry whose digests and signature are all consistent.
func signedItem(t *testing.T, priv ed25519.PrivateKey, id, manifest, script, skill string) PluginItem {
	t.Helper()
	it := PluginItem{
		ID:          id,
		Folder:      id,
		Manifest:    manifest,
		Script:      script,
		Skill:       skill,
		ManifestSHA: digestOf(manifest),
		ScriptSHA:   digestOf(script),
	}
	if skill != "" {
		it.SkillSHA = digestOf(skill)
	}
	msg := SignedStatement(it.ID, it.Folder, it.ManifestSHA, it.ScriptSHA, it.SkillSHA)
	it.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, msg))
	return it
}

// resigned swaps the artifacts and their digests but KEEPS the original signature — an edit by
// somebody who can rewrite the catalog document and cannot sign.
func resigned(t *testing.T, from PluginItem, manifest, script, skill string) PluginItem {
	t.Helper()
	it := from
	it.Manifest, it.Script, it.Skill = manifest, script, skill
	it.ManifestSHA, it.ScriptSHA = digestOf(manifest), digestOf(script)
	it.SkillSHA = ""
	if skill != "" {
		it.SkillSHA = digestOf(skill)
	}
	return it
}

// tweaked returns a copy with one field changed — the shape of a catalog document somebody edited.
func tweaked(it PluginItem, mutate func(*PluginItem)) PluginItem {
	mutate(&it)
	return it
}

func digestOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// A catalog read off this machine may ship an unsigned plugin: there is no channel to authenticate,
// and dropping the folder into plugins/ by hand never needed a signature either. A signature that IS
// present must still verify — somebody tried, and a wrong one means something is off.
func TestVerifySignature_LocalSourceNeedsNoSignature(t *testing.T) {
	pub, priv := testKey(t)
	t.Setenv(devKeyEnv, base64.StdEncoding.EncodeToString(pub))

	unsigned := signedItem(t, priv, "mine", "M", "S", "")
	unsigned.Signature = ""
	if err := verifySignature(unsigned, signaturesOptional); err != nil {
		t.Errorf("verifySignature(local) = %v, want an unsigned local entry to be offered", err)
	}

	_, foreign := testKey(t)
	wrong := signedItem(t, foreign, "mine", "M", "S", "")
	if err := verifySignature(wrong, signaturesOptional); err == nil {
		t.Error("a signature by an untrusted key was accepted from a local catalog")
	}
}
