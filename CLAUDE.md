# CLAUDE.md

Guidance for Claude Code working in this repository.

## Project Overview

`mark` - Go CLI that pushes markdown files to Atlassian Confluence Cloud, creating/updating pages, attachments, folders, and labels. This is a maintained fork of [`kovetskiy/mark`](https://github.com/kovetskiy/mark), carrying the changes a large multi-space Obsidian-style documentation vault needs.

- **Module**: `github.com/kovetskiy/mark/v16`
- **Binary**: `cmd/mark/main.go` -> `/usr/local/bin/mark`
- **Origin**: `git@github.com:hyntelo/mark`
- **Upstream**: `https://github.com/kovetskiy/mark` (wired as the `upstream` remote)
- **Branching**: trunk-based on `master`. Feature work happens on `feat/*` or `fix/*` branches, then ff-merge or rebase to `master`.
- **Upstream sync point**: rebased onto `2d69a79`. Keep this line current when re-syncing.

Upstream also ships `AGENTS.md`, which states its own invariants for the packages the fork does not own. Read it before changing renderer, transformer or manifest code.

## Build & Install

```bash
# Build
go build -o bin/mark ./cmd/mark

# Install system-wide (replaces /usr/local/bin/mark)
sudo install -m 0755 bin/mark /usr/local/bin/mark
mark --version

# All tasks via Taskfile (alternative)
task build
```

`bin/` is not ignored, so a built binary is stageable. Never commit one - a 50 MB blob in the history is not removable by a later commit.

## Test

```bash
# Full suite
go test ./...

# Single package, verbose
go test ./metadata/ -v
go test ./page/ -v

# Single test
go test ./metadata/ -run TestExtractMetaAncestryPreservesFileOrder -v

# Vet (run before commit)
go vet ./...
```

**Unset `MARK_PASSWORD` before running `./util/`.** A shell that exports a real Atlassian token breaks the credential-resolution tests, which assert that a config-file or flag password wins; the env var beats both, so two of them fail and one prints the live token into the output. `env -u MARK_PASSWORD go test ./...`.

Upstream ships an in-memory Confluence fake at `confluence/confluencetest`, which is how resolver behaviour is tested without the network: `confluencetest.New(t)`, then `AddSpace`/`AddPage`/`AddFolder`/`SetHomepage` to arrange a hierarchy, `CountRequests` to assert nothing was created, and `SetFail` to inject a status. `page/ancestry_ordered_test.go` and `page/confluence_id_test.go` use it for the fork's own resolvers.

Two traps in those tests:

- **Reset the folder cache.** `page.ResetFolderCache()` at the top of each test that resolves a folder. The cache is package state keyed by ids the fake hands out afresh per server, so one test's entries resolve another's ids and the failure only appears when the package runs as a whole.
- **Teach the fake, do not work around it.** A missing route (the CQL page search) or a missing field (a page's `space`) is a gap in the double, and papering over it in the test hides the same gap from every later test.

**Comparing behaviour across a rebase or a risky change**: build the old and new binaries side by side and dry-run both over a real vault, diffing rendered HTML and the resolved ancestry per file. That is the only way to catch renderer and resolver regressions, since nothing else exercises the real API.

## Runtime Configuration

- **Auth**: `MARK_PASSWORD` env var (Atlassian API token). User config lives in `~/.config/mark.toml`. Do NOT hardcode tokens or print them - including in shell expansions like `${MARK_PASSWORD:-...}`, which echo the value.
- **Dry run**: `mark --dry-run --log-level DEBUG -f <path.md>` resolves ancestry, prints the rendered HTML, and exits without touching Confluence. `--log-level TRACE` adds the HTTP exchanges, with the credential redacted.
- **Space override**: `--space <KEY>` overrides any `<!-- Space: ... -->` header in the file.
- **CLI parents**: `--parents` prepends parent pages to the resolved ancestry; injected via `meta.Parents` AND `meta.Ancestry`.
- **Features**: `--features` *replaces* the default set (`mermaid`, `mention`) rather than adding to it. `frontmatter` is off by default, which is what makes the fork's front matter skip the path vault files take.

## Architecture

```
cmd/mark/        binary entry; flag parsing -> mark.go::run
mark.go          glue: ExtractMeta -> ResolvePage -> render -> CreatePage/UpdatePage
metadata/        markdown header parsing (Space, Parent, Folder, Title, ...)
page/            ancestry resolution, link rewriting, orphans, ordering, properties
confluence/      REST API client (v1 content/* + v2 pages/folders/spaces), page cache
confluence/confluencetest/  in-memory Confluence fake used by the resolver tests
manifest/        source-file -> published-page store behind --track-pages
report/          run results as JSON or GitHub Actions annotations
attachment/      attachment uploads
markdown/        markdown -> Confluence storage-format renderer
transformer/     Goldmark AST transformers (includes, macros, details, layout, img, links)
renderer/        renderer integration glue
parser/          markdown parser (goldmark wrapper)
includes/        <!-- Include: --> macro support
macro/           <!-- Macro: --> macro support
math/            formula rendering (PNG via Chrome, or SVG)
mermaid/, d2/    diagram pre-rendering (PNG/SVG embedding)
chrome/          shared headless Chrome engine
stdlib/          template stdlib for include/macro
util/            CLI/TOML config + flag parsing
vfs/             virtual filesystem abstraction (memory + os-disk)
```

### Key call chain (page push)

`mark.go::run` -> `metadata.ExtractMeta` -> `page.ResolvePage` -> (`page.EnsureOrderedAncestry` | `page.EnsureAncestry`) -> `confluence.{FindPage,FindPageUnderParent,FindFolder,CreatePage,CreatePageWithFolderParent,CreateFolder}` -> render body -> `confluence.UpdatePage` or new-page POST.

### Ancestry model (fork-divergent)

`ResolvePage` splits on whether the document declares any `<!-- Folder: -->`:

- **No folders** - upstream's path, untouched: `ValidateAncestry` + `EnsureAncestry` over `meta.Parents`.
- **Any folder** - the fork's ordered walker: `metadata.Meta.Ancestry` is the authoritative list of `Ancestor{Type: page|folder, Title}` in the order the headers appear in the file, and `page.EnsureOrderedAncestry` resolves each entry under the previously resolved parent. This supports any `page > folder > page > folder > target` shape; upstream's `EnsureMixedAncestry` resolves all anchor pages first and nests every folder below them, which cannot express interleaving.

`Meta.Parents` and `Meta.Folders` are kept as derived views (CLI parent injection, deterministic title-hash generation). YAML front matter is a map, so order is unobservable there: `Ancestry` then falls back to anchor-pages-then-folders, matching upstream's `EnsureMixedAncestry` semantics.

Resolution details that exist for a reason - do not "simplify" them away:

- Each level is found with a **parent-scoped search**, then a **space-wide search validated against the v2 `parentId`** if that returns nothing, then a **re-resolve if `Create*` fails with a title conflict**. The CQL index lags recent writes, and a publishing wrapper may push files in parallel, so a stale miss is normal rather than exceptional.
- A **leading `<!-- Folder: -->`** (no `Parent` above it) is anchored to the **space homepage**, not the space root: Confluence Cloud parents a space's top-level folders to the homepage. A folder found at the space root is still accepted, for hierarchies created by older versions.
- Once an ancestor is only **simulated** (dry-run), everything below it is simulated too. Feeding a `dry-run-*-id` into a parent-scoped search returns a CQL parse error, not an empty result.
- The walker honours upstream's `AncestryTracker`: every resolved ancestor is recorded under a **type-tagged** chain key (`page:A\x00folder:B`), and an entry whose title no longer resolves is looked up there before another is created. The kind is in the key because the same titles may name a page in one document and a folder in another. A dry run records nothing - its ids name nothing, and the next real run would adopt one as the page the chain means.

When the resolved final parent is a folder, `ResolvePage` encodes a `Type: "folder-parent"` sentinel on the returned `*confluence.PageInfo` so `mark.go::ProcessFile` branches into `CreatePageWithFolderParent`.

### Page identity (fork-divergent)

Upstream identifies a page by `(space, title)` and follows a rename through `manifest/`, a state file written by `--track-pages`. The fork keeps identity **in the document**: `Meta.ID`, from the `confluence_id` front matter key a publishing wrapper stamps back after every push. It survives a fresh clone, needs no state file, and is what a vault actually uses.

- `page.findExistingPage` prefers the ID over the title lookup; `mark.go::ProcessFile` then writes `meta.Title` onto the resolved page so `UpdatePage` renames it in place.
- The ID is read out of the **skipped** front matter block too (`metadata.confluenceIDFromFrontMatter`), because vault files carry their Mark metadata in header comments and the front matter is never decoded.
- Only a **404** falls back to the title lookup. Any other failure to read - a 401, a 403, a 5xx past its retries - ends the run, because falling back there publishes a duplicate and the run that did it looks like it worked.
- An ID resolving into a **different space** also falls back, with a warning. Never let that through: overwriting an unrelated page is unrecoverable, a duplicate is not. The check needs `GetPageByID` to expand `space`.
- The rename rides on upstream's `titleChanged` flag rather than a second one of its own, so `--changes-only` publishes despite an identical body and the `--preserve-comments` refetch does not drop the new title.

## Fork Divergences from Upstream

Carried in `master`, not yet upstreamed:

| Commit | Area | What |
|---|---|---|
| `78ec96e` | mark.go | When a `-f` glob matches nothing, fall back to the literal path |
| `e0dc0c8` | page/link.go | Decode a percent-encoded link target before looking for the file |
| `9421123` | util/flags | `--layout` flag, and naming a config key nothing reads |
| `852e7ab` | metadata | Ordered `Meta.Ancestry` preserving the order Parent/Folder headers appear in |
| `802bf88` | confluence | `FindPageUnderParent` (parent-scoped CQL) + `GetPageParentInfo` (v2 arbiter) |
| `226d653` | page | `EnsureOrderedAncestry`: mixed ancestry resolved in declaration order, tracker-aware |
| `783d438` | metadata | Skip Obsidian front matter before scanning Mark headers |
| `6264de1` | page | Anchor leading `Folder` headers to the space homepage; stop descending past simulated ancestors |
| `2f97b9a` | metadata, page, mark.go | `confluence_id` front matter key: resolve the page by ID and rename it in place |

What the fork used to carry and **upstream now owns** - do not re-add local copies, extend upstream's helpers:

- Folder support in full (`resolveFolder`, `EnsureFolderAncestry`, `EnsureMixedAncestry`, `CreatePageWithFolderParent`, the folder cache, the space-root relocation).
- Leaving a link alone when the file it names declares no title (`resolveLink` checks `linkMeta.Title`), which is why that fix is no longer in the table.
- Link rewriting on the AST rather than on the file's text (`transformer/links.go`), which removed the regex the fork's multi-link fix patched.
- Front matter parsing and stripping (`stripFrontMatter`), behind `--features frontmatter`, and key normalisation (`normaliseFrontMatterKey`).
- A retitle surviving `--changes-only` and the `--preserve-comments` refetch (`titleChanged`).

Coordinate upstreaming via the `upstream` remote (GitHub PR). Good candidates, in order: the link decode (`e0dc0c8`), the front matter skip (`783d438`), then ordered ancestry as a feature PR.

## Confluence API Notes

- **REST v1** (`content/*`): search (CQL), find by title, ancestor expansion, page create/update. Results are cached per `(space, title, type)` by the client.
- **REST v2** (`pages/*`, `folders/*`): folder operations and `parentId/parentType` reads. Folders only exist in Cloud and only via v2.
- **CQL is index-lagged and not authoritative.** `parent="..."` and `ancestor=...` both miss recent writes; always pair with a v2 `parentId` check - see `page/ancestry.go::resolvePageBySpaceWideAndValidate`.
- **`FindFolder(space, title, underAncestorID)` matches any ancestor, not the direct parent.** `resolveFolder` re-checks `ParentID` before accepting the result.
- **`FindPage` with an empty title matches an arbitrary page** in the space. Never call it, directly or transitively, without a title - that was the cause of every attachment link being rewritten to one random page.
- **A failed read is not a thing that is gone.** `GetPageByID` answers a 404 with `confluence.ErrNotFound` and `GetFolderByID` answers one with `(nil, nil)`; every other error is a failure to read. Treating those as absence creates a duplicate page or splits a folder hierarchy, silently, in a run that reports success.
- **CreatePageWithFolderParent** vs **CreatePage**: distinct endpoints. Folder-parented pages MUST go through the v2 path with `parentType: "folder"`.

## Vault Integration

The fork is driven by a thin wrapper that walks a documentation vault and calls `mark` per file. Every `.md` file carries a `<!-- Space: X -->` comment so `page/link.go` can resolve cross-space inter-doc links, and Obsidian front matter above it (hence the front matter skip).

That wrapper passes only `-f`, `--space`, `--dry-run` and `--edit-lock`, and scrapes stdout for `/pages/<id>` to stamp `confluence_id` back into the file's front matter. Changing what `mark` prints on success breaks that stamping. The stamped ID is no longer write-only: `mark` reads it back to rename pages in place (see Page identity above).

Common workflow when changing mark's behavior:
1. Build + install patched binary (`go build -o bin/mark ./cmd/mark && sudo install -m 0755 bin/mark /usr/local/bin/mark`).
2. Validate via dry-run: `mark --log-level DEBUG --dry-run --space DOCS -f "<some vault file>"`.
3. Push end-to-end through the wrapper against a scratch space.
4. Spot-check Confluence to confirm parent/folder resolution.

## Conventions

- **Commit format**: conventional commits with type+scope (e.g., `feat(page): ...`, `fix(metadata): ...`, `docs(README): ...`). Use HEREDOC for multi-line bodies.
- **Imports**: keep `metadata` import in `page/ancestry.go`. The walker depends on `metadata.Ancestor`/`AncestorPage`/`AncestorFolder`.
- **Error messages**: when title-conflict ambiguity surfaces, return actionable errors that name the parent IDs (see `resolvePageBySpaceWideAndValidate` for the template).
- **Do not reformat upstream code.** `gofmt -w` on a file upstream leaves unformatted (e.g. `mark.go`) adds diff noise and future conflicts. Format only the lines the fork touches.
- **No `--no-verify`**: hooks and signing must run on commits.

## Things to NOT Touch

- The `metadata/metadata.go` title-hash generator (`titleAppendGeneratedHash`, ~line 824) reads `meta.Parents`, not `Ancestry`. This is a deterministic legacy hash contract; changing it breaks page lookups for files published with `--title-append-generated-hash`.
- The `ParentInfo` struct in `page/ancestry.go` - upstream's, used by `EnsureFolderAncestry`. The fork's walker uses `OrderedParent`.
- Upstream-style tests under `testdata/` - mostly snapshot-based; modify with care and regenerate snapshots only when intentional.
- The **mermaid dependency pin is gone**. The fork used to hold `dreampuf/mermaid.go` at `v0.0.40` with a hand-rolled chromedp viewport to fix `architecture-beta` layout; the vault it serves has no `architecture-beta` diagrams (architecture diagrams go through D2 instead), so the pin was dropped in favour of upstream's shared Chrome engine. If `architecture-beta` comes back and renders squished, pass `chromedp.WindowSize(...)` to the render engine - it accepts allocator options now - rather than restoring the fork's custom renderer.

## Useful Commands

```bash
# What changed locally vs. origin/master
git log --oneline origin/master..HEAD

# Fork's divergence from upstream
git log --oneline upstream/master..HEAD

# Pull upstream and rebase
git fetch upstream && git rebase upstream/master

# Re-run a specific test with race detector
go test ./page/ -run TestEnsureOrderedAncestry -race -v
```
