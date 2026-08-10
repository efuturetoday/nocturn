# A sample catalog

What `NOCTURN_CATALOG_URL` points at: the curated list the app's library browses and installs from.
Three skills and two MCP servers, enough to see the whole path work.

Serve it and start the daemon against it:

```sh
python3 -m http.server 9000 --directory examples/catalog &
NOCTURN_CATALOG_URL=http://127.0.0.1:9000/catalog.json nocturn serve
```

Unset the variable and the library is **absent** rather than empty — nothing is fetched and no
request leaves the machine for it.

## The shape

```json
{
  "schemaVersion": 1,
  "version": "2026-08-10",
  "skills": [{ "id": "…", "title": "…", "description": "…", "homepage": "…", "tags": ["…"],
               "folder": "…", "body": "the whole SKILL.md", "sha256": "…of body" }],
  "mcp":    [{ "id": "…", "title": "…", "description": "…", "homepage": "…", "tags": ["…"],
               "name": "…", "url": "https://…", "auth": "oauth", "oauth": { … } }]
}
```

Two things about it are load-bearing rather than convenient:

**A skill's body is inline.** The catalog is the only place installing fetches from. A catalog that
listed URLs would make every listed URL a trust anchor and the daemon something that fetches from
strangers.

**`sha256` is over the body, and is checked before anything is written.** It authenticates nothing —
whoever serves the catalog serves the digest too — so it is not a signature and the daemon does not
treat it as one. What it does is turn a truncated or garbled response into a refusal instead of a
half-installed skill, and it is the field a signature will be computed over when there is one.

The daemon is strict with the rest: an unknown `schemaVersion` or an unknown field refuses the whole
catalog, and a single malformed entry is dropped rather than taking the catalog down. A dropped entry
simply is not offered, which is the fail-closed side.

## Regenerating after an edit

Change a body and its digest has to change with it, or the daemon drops the entry:

```sh
python3 - <<'PY'
import hashlib, json
cat = json.load(open("examples/catalog/catalog.json"))
for s in cat["skills"]:
    s["sha256"] = hashlib.sha256(s["body"].encode()).hexdigest()
json.dump(cat, open("examples/catalog/catalog.json", "w"), indent=2)
PY
```
