// Package catalog holds the source of the published library catalog — the skills and MCP servers a
// fresh daemon offers before anybody configures anything.
//
// The package itself is empty on purpose. What matters is the tree beside this file:
//
//	skills/<folder>/SKILL.md    the body, verbatim what an install writes to disk
//	skills/<folder>/entry.json  how the entry is listed: title, tags, homepage
//	mcp/<name>.json             one remote server declaration, plus how it is listed
//	generate.go                 builds docs/public/catalog.json from the above
//	import.go                   pulls candidates from the MCP registry, for a human to curate
//
// Skills are kept as real directories rather than as strings inside a JSON file because the JSON is
// what rots: a body edited in place without its sha256 recomputed is dropped by the daemon SILENTLY
// (library.validSkills), so a catalog nobody generates is a catalog whose entries quietly disappear.
// Here the digest is computed, the file is reviewable as Markdown, and catalog_test.go rehearses the
// install of every entry — a skill that could not be installed cannot be published.
//
// The generated file lives under docs/public/ so the docs workflow publishes it with the site; that is
// the URL library.DefaultURL points at.
package catalog

//go:generate go run generate.go
