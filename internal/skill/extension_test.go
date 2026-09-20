package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/efuturetoday/nocturn/internal/skill"
)

// A skill's body may reference the config its manifest declares, and the values are substituted on
// the way into the catalog — the model reads an address, never a placeholder.
func TestConfiguredSkillBodyIsRendered(t *testing.T) {
	dir := writeConfigurableSkill(t, "https://hass.example.com")

	set, _ := skill.Discover(dir, nil)
	sk, ok := set["house"]
	if !ok {
		t.Fatalf("skill not discovered: %v", set)
	}
	if !strings.Contains(sk.Body, "https://hass.example.com/api/") {
		t.Errorf("the address was not substituted into the body: %q", sk.Body)
	}
	if strings.Contains(sk.Body, "{{config.") {
		t.Errorf("an unrendered placeholder survived: %q", sk.Body)
	}
}

// An unconfigured skill stays in the catalog and says what is missing. Hiding it would have the
// assistant deny it can do the thing, with no hint that one command fixes it — the same reason a
// plugin's tools are exposed before its account is connected.
func TestUnconfiguredSkillSaysWhatIsMissing(t *testing.T) {
	dir := writeConfigurableSkill(t, "")

	set, _ := skill.Discover(dir, nil)
	sk, ok := set["house"]
	if !ok {
		t.Fatalf("an unconfigured skill vanished from the catalog: %v", set)
	}
	if !strings.Contains(sk.Body, "base_url") || !strings.Contains(sk.Body, "nocturn config house") {
		t.Errorf("the body does not name the missing setting and the command: %q", sk.Body)
	}
	if strings.Contains(sk.Body, "/api/") {
		t.Errorf("the real body survived without an address to call: %q", sk.Body)
	}
}

// The extension control plane sits in the same folder as the skill's own files. It is neither listed
// as a bundled resource nor readable through skill_read: the shard is credential material, and the
// manifest and config are the answer to "what may this skill do", which is not the model's research.
func TestControlPlaneFilesAreNotResources(t *testing.T) {
	dir := writeConfigurableSkill(t, "https://hass.example.com")
	if err := os.WriteFile(filepath.Join(dir, "house", "secrets.enc"), []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "house", "reference.md"), []byte("real resource"), 0o600); err != nil {
		t.Fatal(err)
	}

	set, dirs := skill.Discover(dir, nil)
	body := set["house"].Body
	for _, hidden := range []string{"manifest.json", "config.json", "secrets.enc"} {
		if strings.Contains(body, hidden) {
			t.Errorf("%s is advertised as a bundled resource: %q", hidden, body)
		}
	}
	if !strings.Contains(body, "reference.md") {
		t.Errorf("a real bundled file is missing from the listing: %q", body)
	}

	tool, err := skill.ReadTool(dirs)
	if err != nil {
		t.Fatal(err)
	}
	for _, hidden := range []string{"manifest.json", "config.json", "secrets.enc"} {
		if _, err := tool.Call(t.Context(), `{"name":"house","path":"`+hidden+`"}`); err == nil {
			t.Errorf("skill_read returned %s", hidden)
		}
	}
	if _, err := tool.Call(t.Context(), `{"name":"house","path":"reference.md"}`); err != nil {
		t.Errorf("skill_read refused a real resource: %v", err)
	}
}

// writeConfigurableSkill installs a skill declaring one setting its body references. An empty baseURL
// leaves it unconfigured.
func writeConfigurableSkill(t *testing.T, baseURL string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "house")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: house\ndescription: Talk to the household server.\n---\nGET {{config.base_url}}/api/ first.\n"
	writeFile(t, filepath.Join(dir, "SKILL.md"), body)
	writeFile(t, filepath.Join(dir, "manifest.json"), `{"config":[{"name":"base_url","type":"url","label":"Address"}],
		"credentials":[{"name":"token","host":"{{config.base_url}}","header":"Authorization","prefix":"Bearer "}]}`)
	if baseURL != "" {
		writeFile(t, filepath.Join(dir, "config.json"), `{"base_url":"`+baseURL+`"}`)
	}
	return root
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
