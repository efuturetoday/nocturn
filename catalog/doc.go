// Package catalog holds the source of the published library catalog — the skills and MCP servers a
// fresh daemon offers before anybody configures anything.
//
// The package itself is empty on purpose. What matters is the tree beside this file:
//
//	extensions/<name>/          one installable thing, holding exactly what an install writes:
//	  manifest.json               what it needs before it works (optional)
//	  SKILL.md                    instructions (optional)
//	  plugin.json + plugin.js     code for the sandbox (optional)
//	  mcp.json                    a remote server declaration (optional)
//	  entry.json                  how it is listed: title, tags, homepage, serial
//	  extension.sig               the signature, committed; CI never holds the key
//	entryread.go                shared reading, so what is signed is what is published
//	generate.go                 builds docs/public/catalog.json from the above
//	sign.go                     writes extension.sig for an entry
//	import.go                   pulls candidates from the MCP registry, for a human to curate
//
// Entries are kept as real directories rather than as strings inside a JSON file because the JSON is
// what rots: a body edited in place without its sha256 recomputed is dropped by the daemon SILENTLY
// (library.validItems), so a catalog nobody generates is a catalog whose entries quietly disappear.
// Here the digest is computed, every file is reviewable as itself, and catalog_test.go rehearses the
// install of every entry — something that could not be installed cannot be published.
//
// One folder per THING, not per kind: an integration that brings a server and the instructions for it
// is one entry with one signature, so nobody can install half of it.
//
// The generated file lives under docs/public/ so the docs workflow publishes it with the site; that is
// the URL library.DefaultURL points at.
package catalog

//go:generate go run generate.go entryread.go
