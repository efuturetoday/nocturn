# Serving your own catalog

A daemon already has one: unset, `NOCTURN_CATALOG_URL` means the curated catalog this project
publishes, and `off` means no library at all. What follows is for pointing it somewhere else — a
catalog of your own skills, or the published one served locally while you work on it.

A catalog on this machine is a **path**, not a URL — no web server involved:

```sh
go generate ./catalog/                                   # after editing anything under catalog/
NOCTURN_CATALOG_URL=./docs/public/catalog.json nocturn serve
```

A remote one must be `https://`, and a redirect that leaves the scheme or the host is refused: there
the channel is the whole of what says these bytes are the catalog. A file has no channel, which is
also why a plugin in it needs no signature — dropping a folder into `plugins/` never did either.

## The shape

```json
{
  "schemaVersion": 1,
  "version": "…",
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
is named in the daemon's log and simply is not offered, which is the fail-closed side.

## Do not hand-maintain the JSON

Edit a body and its digest has to change with it, or the entry is dropped. That is why the published
catalog has a generator and a test that rehearses every install — see
[`catalog/doc.go`](../../catalog/doc.go). Build yours the same way rather than editing JSON by hand;
one forgotten `sha256` is one skill that quietly stops existing.

## Plugins in your own catalog

A plugin entry from a REMOTE catalog must carry an Ed25519 signature by a key compiled into the
daemon, so a catalog host cannot ship code nobody vouched for. Two ways to ship your own:

- serve the catalog as a **file path** — no signature required, because there is no channel to
  authenticate and you could have copied the folder into `plugins/` yourself;
- or mint a key (`go run catalog/sign.go -keygen`), sign with it, and set
  `NOCTURN_CATALOG_DEV_KEY` to its public half on the daemons that should trust you.
