package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/efuturetoday/nocturn/agentkit"
	"github.com/efuturetoday/nocturn/internal/discovery"
	"github.com/efuturetoday/nocturn/internal/extension"
	"github.com/efuturetoday/nocturn/internal/mcp"
	"github.com/efuturetoday/nocturn/internal/plugin"
	"github.com/efuturetoday/nocturn/internal/secret"
)

// This file walks the one tree an installed thing lives in, and it is the only place a credential is
// registered. Before it there were four registration paths — a plugin's manifest, an MCP connection's
// own binding, a hand-written bindings.json and a NOCTURN_SECRET_* environment channel — which meant
// four answers to "what happens to this credential when the thing that declared it is deleted", and
// only two of them had one.

// installed is one extension on disk: its name, the complete declaration assembled from every payload
// in its folder, and what has been configured for it. The payloads themselves are read by the packages
// that own them; what this type holds is the half every installed thing shares.
//
// What a folder CARRIES is not among them: it decides whether the folder is an extension at all, and
// that question is answered where it is asked. A field nothing reads is a fact nothing keeps true.
//
// Unexported: it is what one discovery pass hands to the next step inside this package. What leaves
// the package is the declaration (DeclOf) and the inventory, never this.
type installed struct {
	Name   string
	Decl   extension.Decl
	Values extension.Values
}

// Owner is the credential-injection owner of this extension.
func (i installed) owner() string { return extension.Owner(i.Name) }

// payloadFiles is what makes a folder carry a payload: the file whose presence says so.
var payloadFiles = map[extension.Payload]string{
	extension.PayloadSkill:  "SKILL.md",
	extension.PayloadPlugin: plugin.ManifestFile,
	extension.PayloadMCP:    mcp.ConfigFile,
}

// foldPayloads completes a folder's declaration with what its payloads declare beside manifest.json:
// a plugin states its credentials in its own manifest, because there they are covered by the
// signature over the artifact, and a server's bearer is derived from its URL rather than written
// down — the URL IS the declaration.
//
// ONE helper rather than a fold per caller. Three partial assemblies is how "what does this extension
// declare" comes to have three answers, and the failure they produce is silent in both directions:
// an assembler missing the MCP half binds no bearer while `secret set` still resolves its key, so a
// token is seeded into a shard that nothing ever injects.
//
// srv is nil for a folder that carries no server. A plugin that will not load contributes nothing —
// the plugin discovery pass reports it; here it is one payload's declaration missing, not a reason to
// drop the rest of the folder's.
func foldPayloads(d extension.Decl, dir string, srv *mcp.Server) extension.Decl {
	if loaded, err := plugin.Load(dir); err == nil {
		d.Config = append(d.Config, loaded.Manifest.Config...)
		d.Credentials = append(d.Credentials, loaded.Manifest.Credentials...)
	}
	if srv != nil {
		d.Credentials = append(d.Credentials, srv.Decl().Credentials...)
	}
	return d
}

// serverNamed picks the declared server belonging to one folder, or nil. The FOLDER is the identity
// here, as it is for the owner and the shard key — mcp.Read sets it the same way.
func serverNamed(servers []mcp.Server, name string) *mcp.Server {
	for i := range servers {
		if servers[i].Name == name {
			return &servers[i]
		}
	}
	return nil
}

// discoverExtensions walks the extensions tree and reads every folder's declaration and configuration.
//
// What a folder CARRIES is read off the files in it — a SKILL.md makes it a skill, a plugin.json a
// plugin, an mcp.json a server — and a folder may carry several, which is the point of one tree: a
// household installs "Home Assistant", not "a skill plus a server that happen to share a name".
//
// A folder whose declaration or configuration will not load is reported and kept with whatever did
// load: its credentials are then simply absent (fail closed) while its payloads still work
// unauthenticated, which is the state somebody has to SEE in order to fix it.
func discoverExtensions(dir string, servers []mcp.Server, diag *agentkit.Diagnostics) []installed {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []installed
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !extension.ValidName(e.Name()) {
			discovery.Diagnose(diag, "extension:"+e.Name(), "skipped (the name is an owner and a shard key)")
			continue
		}
		folder := filepath.Join(dir, e.Name())
		carries := map[extension.Payload]bool{}
		for payload, file := range payloadFiles {
			if _, err := os.Stat(filepath.Join(folder, file)); err == nil {
				carries[payload] = true
			}
		}
		if len(carries) == 0 {
			continue // a folder with no payload is not an extension, it is a directory
		}
		inst := installed{Name: e.Name(), Values: extension.Values{}}

		decl, err := extension.LoadDecl(folder)
		if err != nil {
			discovery.Diagnose(diag, "extension:"+e.Name(), "manifest ignored: "+err.Error())
		}
		inst.Decl = foldPayloads(decl, folder, serverNamed(servers, e.Name()))

		values, err := extension.LoadValues(folder, inst.Decl)
		if err != nil {
			discovery.Diagnose(diag, "extension:"+e.Name(), "config ignored: "+err.Error())
		} else {
			inst.Values = values
		}
		out = append(out, inst)
	}
	return out
}

// bindExtensions hands the injector every installed extension's credential bindings, and returns the
// owners it bound.
//
// Everything is staged first and handed over in ONE call (secret.Injector.SetOwned), which is where
// the three hazards live that this used to guard against with comments: a reload injecting the same
// credential twice, a binding outliving the extension that declared it, and a window in which an
// in-flight request finds no binding and leaves unauthenticated. Staging also means a declaration
// that cannot resolve its host does not take the rest down with it.
func (p pass) bindExtensions(exts []installed) []string {
	staged := make(map[string][]secret.Binding, len(exts))
	owners := make([]string, 0, len(exts))
	for _, e := range exts {
		bindings, err := e.Decl.Bindings(e.owner(), e.Values)
		if err != nil {
			// A credential whose host is not configured yet is not an error to fail the pass on — it
			// is an extension somebody installed and has not finished setting up. It simply has no
			// binding until it does, and the skill body says so.
			discovery.Diagnose(p.diag, e.owner(), "no credential binding: "+err.Error())
			continue
		}
		if len(bindings) == 0 {
			continue
		}
		staged[e.owner()] = bindings
		owners = append(owners, e.owner())
	}
	if p.injector == nil {
		return owners
	}
	p.injector.SetOwned(staged)
	return owners
}

// ExtensionDirs are the workspace subdirectories a secret shard can live in. One tree, one entry —
// kept as a list because secret.LoadShardsInto takes the trees to walk and stays mechanism-only.
func ExtensionDirs() []string { return []string{extension.Dir} }

// DeclOf reads one installed extension's declaration and its configured values, by name. It is the
// single place that knows where each payload writes what it declares: manifest.json for the shared
// half, a plugin's own manifest for its credentials, an mcp.json's URL for its bearer.
func DeclOf(wsDir, name string) (extension.Decl, extension.Values, error) {
	root := filepath.Join(wsDir, extension.Dir)
	dir := filepath.Join(root, name)
	if _, err := os.Stat(dir); err != nil {
		return extension.Decl{}, nil, fmt.Errorf("no extension %q in this workspace", name)
	}
	decl, err := extension.LoadDecl(dir)
	if err != nil {
		return extension.Decl{}, nil, err
	}
	var srv *mcp.Server
	if s, err := mcp.Read(root, name); err == nil {
		srv = &s
	}
	decl = foldPayloads(decl, dir, srv)
	values, err := extension.LoadValues(dir, decl)
	if err != nil {
		return extension.Decl{}, nil, err
	}
	return decl, values, nil
}

// SetConfig validates and stores one extension's config values in its folder's config.json. It never
// touches a declaration: a person supplies VALUES, and which credential goes to which host stays
// whatever was installed — for a plugin that manifest is signed, which is exactly why the values live
// in a file of their own.
func SetConfig(wsDir, name string, values extension.Values) error {
	decl, _, err := DeclOf(wsDir, name)
	if err != nil {
		return err
	}
	if len(decl.Config) == 0 {
		return fmt.Errorf("%s declares no settings", name)
	}
	return extension.SaveValues(filepath.Join(wsDir, extension.Dir, name), decl, values)
}

// CredentialHosts are the hosts one installed extension's credentials are bound to, resolved through
// its config. It is what a removal reads BEFORE deleting the folder — the declaration is the only
// place those hosts are written down, and a grant that outlives the thing it was given for is a
// permission the next thing on that host inherits without anyone granting it.
func CredentialHosts(wsDir, name string) []string {
	decl, values, err := DeclOf(wsDir, name)
	if err != nil {
		return nil
	}
	var hosts []string
	for _, c := range decl.Credentials {
		host, err := decl.ResolveHostFor(c, values)
		if err != nil || host == "" {
			continue
		}
		hosts = append(hosts, host)
	}
	return hosts
}

// ExtensionCredentialHosts is CredentialHosts for this workspace — the form the serve layer needs
// before it removes something.
func (w *Workspace) ExtensionCredentialHosts(name string) []string {
	return CredentialHosts(w.dir, name)
}
