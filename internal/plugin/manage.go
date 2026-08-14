package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/efuturetoday/nocturn/internal/discovery"
)

// File names inside a plugin's folder. Exported because installing one means writing exactly these.
const (
	ManifestFile = "plugin.json"
	ScriptFile   = "plugin.js"
	// SkillFile is the optional instructions a plugin bundles: when to reach for its tools, and what
	// its arguments really take. Optional because a plugin whose tools are self-explanatory needs
	// none, and a skill nobody needed would cost prompt space in every turn.
	SkillFile = "SKILL.md"
)

// Write installs a JS plugin: <dir>/<folder>/{plugin.json,plugin.js}, where dir is a workspace's
// plugins/ folder.
//
// Both files land or neither does, and the manifest is validated first — through Load, the same
// reader Discover uses, so a plugin that would be skipped at startup is refused here instead. A
// plugin that silently never appears is the one failure a person cannot act on, and it is worse for a
// plugin than for anything else: its tools simply are not there, and the model then says it cannot do
// the thing rather than that it was not installed.
//
// It refuses to overwrite. A plugin's folder is also where its secret shard lives, so replacing the
// code under an existing name would point a credential that was authorized for one program at
// another one — the same argument mcp.Write makes about a server's URL.
//
// WASM plugins are not installable this way on purpose: the catalog carries text, and a base64
// artifact in a JSON document is a different review surface (and a different size class) than a
// readable plugin.js. Sideloading a folder is still how one arrives.
func Write(dir, folder, manifest, script, bundledSkill string) (Manifest, error) {
	if !discovery.ValidName(folder) {
		return Manifest{}, fmt.Errorf("plugin: invalid folder %q", folder)
	}
	if manifest == "" || script == "" {
		return Manifest{}, errors.New("plugin: a manifest and a plugin.js are both required")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Manifest{}, err
	}
	// Creating the folder IS the claim on the name, the way mcp.Write claims a server's: os.Mkdir
	// fails when the path exists, so two installs of the same plugin cannot both get past here. A
	// Stat-then-MkdirAll would let both through the check, and the second would overwrite the first
	// plugin's code under a folder whose secret shard was authorized for the first.
	target := filepath.Join(dir, folder)
	if err := os.Mkdir(target, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Manifest{}, fmt.Errorf("plugins/%s already exists", folder)
		}
		return Manifest{}, err
	}
	// Written before validation, because Load reads from disk — and removed again if it refuses, so a
	// rejected plugin leaves nothing behind holding its name.
	if err := os.WriteFile(filepath.Join(target, ManifestFile), []byte(manifest), 0o600); err != nil {
		_ = os.RemoveAll(target)
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(target, ScriptFile), []byte(script), 0o600); err != nil {
		_ = os.RemoveAll(target)
		return Manifest{}, err
	}
	if bundledSkill != "" {
		if err := os.WriteFile(filepath.Join(target, SkillFile), []byte(bundledSkill), 0o600); err != nil {
			_ = os.RemoveAll(target)
			return Manifest{}, err
		}
	}
	loaded, err := Load(target)
	if err != nil {
		_ = os.RemoveAll(target)
		return Manifest{}, err
	}
	return loaded.Manifest, nil
}

// Remove deletes a plugin's folder — its manifest, its artifact, and the secret shard beside them.
// Dropping the credential material with the code is the point: an uninstall that left a token behind
// would leave the next plugin of that name inheriting it.
func Remove(dir, folder string) error {
	if !discovery.ValidName(folder) {
		return fmt.Errorf("plugin: invalid folder %q", folder)
	}
	target := filepath.Join(dir, folder)
	if _, err := os.Stat(target); err != nil {
		return fmt.Errorf("no plugin %q", folder)
	}
	return os.RemoveAll(target)
}
