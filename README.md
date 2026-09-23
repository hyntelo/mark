# mark (Hyntelo fork)

Fork of [`kovetskiy/mark`](https://github.com/kovetskiy/mark), maintained for a large
multi-space Obsidian-style documentation vault.

- Full usage reference (headers, flags, macros, includes): [upstream README](https://github.com/kovetskiy/mark#readme)
- Internals and invariants of the fork: [CLAUDE.md](./CLAUDE.md)

---

## 1. What mark is

A CLI that publishes Markdown files to Atlassian Confluence Cloud.

For each file it:

1. reads the metadata headers (`<!-- Space: -->`, `<!-- Parent: -->`, `<!-- Folder: -->`, `<!-- Title: -->`, ...);
2. finds or creates the page, its parent pages and its folders;
3. renders Markdown to Confluence storage format (mermaid, d2, macros, includes);
4. uploads attachments and labels;
5. updates the page through the REST API.

```markdown
<!-- Space: DOCS -->
<!-- Parent: Architecture -->
<!-- Folder: Backend -->
<!-- Title: Authentication -->

# Authentication
...
```

```bash
mark -f docs/auth.md                       # publish
mark --dry-run --log-level DEBUG -f docs/auth.md   # render and resolve, touch nothing
```

---

## 2. What the fork changes

| Area | Upstream | Fork |
|---|---|---|
| Mixed ancestry | anchor pages first, then every folder below them | `Parent` / `Folder` headers resolved **in the order they appear** (`page > folder > page > folder > target`) |
| Leading `Folder` | parented to the space root | parented to the **space homepage**, as Confluence Cloud does |
| Lookup of each level | CQL search | parent-scoped search → space-wide search validated on v2 `parentId` → re-resolve on title conflict (CQL index lags) |
| Page identity | `(space, title)`, or a `--track-pages` state file | `confluence_id` in the front matter: page found by ID and **renamed in place** |
| Obsidian front matter | parsed only with `--features frontmatter` | skipped before scanning headers; `confluence_id` still read from it |
| Links | literal target | percent-encoded targets (`My%20Page.md`) decoded before lookup |
| `-f` glob | matches nothing → nothing | matches nothing → literal path |
| Layout | none | `--layout` flag |
| Renderers | Chrome only | Chrome **or** native renderers (merman-cli, resvg), see §3 |

---

## 3. Renderers

Diagrams are drawn by an external program found on the `PATH`. The release archive
contains only `mark`.

| Feature | Chrome (default) | Native (no browser) |
|---|---|---|
| `mermaid` | `--mermaid-engine=chrome` | `--mermaid-engine=merman` → needs `merman-cli` |
| `d2` (PNG) | `--d2-engine=chrome` | `--d2-engine=resvg` → needs `resvg` |
| `math` | Chrome | not available |

- Documents without diagrams need nothing installed.
- Pick the engine with a flag, an env var or `mark.toml` (§5.2): see §4.3, step 4.
- merman-cli must be at least the version in [`mermaid/merman-version.txt`](./mermaid/merman-version.txt).

**Which one?**

- **Chrome**: complete (maths too), faithful to mermaid.js, ~280 MB of browser.
- **Native**: small, no browser, fast; merman is experimental and ignores `themeVariables` in `%%{init}%%` (use `--mermaid-config`).

---

## 4. Installation

### 4.1 Binary

Download from [Releases](https://github.com/hyntelo/mark/releases):

| OS | Archive |
|---|---|
| Linux x86_64 | `mark_Linux_x86_64.tar.gz` |
| Linux arm64 | `mark_Linux_arm64.tar.gz` |
| macOS Intel | `mark_Darwin_x86_64.tar.gz` |
| macOS Apple Silicon | `mark_Darwin_arm64.tar.gz` |
| Windows x86_64 | `mark_Windows_x86_64.zip` |
| Windows arm64 | `mark_Windows_arm64.zip` |

**Linux / macOS**

```bash
tar -xzf mark_<Os>_<Arch>.tar.gz mark
sudo install -m 0755 mark /usr/local/bin/mark
mark --version
```

macOS, if Gatekeeper blocks it: `xattr -d com.apple.quarantine /usr/local/bin/mark`

**Windows** (PowerShell)

```powershell
Expand-Archive mark_Windows_x86_64.zip -DestinationPath "$env:LOCALAPPDATA\mark"
[Environment]::SetEnvironmentVariable("Path", "$env:Path;$env:LOCALAPPDATA\mark", "User")
# open a new terminal
mark --version
```

### 4.2 Chrome variant (default)

Install Chrome or Chromium in its standard location; mark finds it on its own.

| OS | Command |
|---|---|
| Debian / Ubuntu | `sudo apt install chromium` (or Google Chrome `.deb`) |
| Arch | `sudo pacman -S chromium` |
| macOS | `brew install --cask google-chrome` |
| Windows | `winget install Google.Chrome` |

Nothing to configure: `chrome` is the default for both engines.

### 4.3 Native variant (merman-cli + resvg)

**Step 1 — merman-cli** (mermaid)

| OS | How |
|---|---|
| Linux x86_64 | prebuilt from [merman releases](https://github.com/Latias94/merman/releases) (below) |
| other OS / arch | a build from the releases page if there is one, otherwise `cargo install merman-cli` |

**Step 2 — resvg** (d2)

| OS | Command |
|---|---|
| Linux | `cargo install --locked resvg` (no prebuilt Linux binary) |
| macOS | `brew install resvg` |
| Windows | `resvg-win64.zip` from [resvg releases](https://github.com/linebender/resvg/releases), add to `PATH` |

**Step 3 — d2 fonts** (so resvg draws with the fonts d2 measured the text with)

Download the fonts of the d2 version mark is built with (`v0.9.0`, see `go.mod`).

Linux / macOS:

```bash
D="$HOME/.local/share/fonts/d2"; ok=y
if [ -n "$(ls -A "$D" 2>/dev/null)" ]; then
  printf '%s is not empty, delete its contents and download again? [Y/n] ' "$D"; read -r ans
  case "$ans" in [nN]*) ok=n ;; *) rm -rf "$D" ;; esac
fi
if [ "$ok" = y ]; then
  mkdir -p "$D"
  for f in SourceSansPro-{Regular,Bold,Semibold,Italic} SourceCodePro-{Regular,Bold,Semibold,Italic} FuzzyBubbles-{Regular,Bold}; do
    curl -fsSL -o "$D/$f.ttf" "https://raw.githubusercontent.com/terrastruct/d2/v0.9.0/d2renderers/d2fonts/ttf/$f.ttf"
  done
  fc-cache -f 2>/dev/null   # Linux only
fi
```

Windows (PowerShell):

```powershell
$D = "$env:LOCALAPPDATA\mark\fonts"; $ok = $true
if ((Test-Path $D) -and (Get-ChildItem -Force $D)) {
  $ans = Read-Host "$D is not empty, delete its contents and download again? [Y/n]"
  if ($ans -match '^[nN]') { $ok = $false } else { Remove-Item -Recurse -Force $D }
}
if ($ok) {
  New-Item -ItemType Directory -Force $D | Out-Null
  "SourceSansPro-Regular","SourceSansPro-Bold","SourceSansPro-Semibold","SourceSansPro-Italic",
  "SourceCodePro-Regular","SourceCodePro-Bold","SourceCodePro-Semibold","SourceCodePro-Italic",
  "FuzzyBubbles-Regular","FuzzyBubbles-Bold" | ForEach-Object {
    Invoke-WebRequest "https://raw.githubusercontent.com/terrastruct/d2/v0.9.0/d2renderers/d2fonts/ttf/$_.ttf" -OutFile "$D\$_.ttf"
  }
}
```

**Step 4 — select the engines**

Three equivalent ways, pick one (a flag beats the env var, the env var beats the file):

| Way | mermaid | d2 |
|---|---|---|
| Flag, per run | `--mermaid-engine=merman` | `--d2-engine=resvg` |
| Env var | `MARK_MERMAID_ENGINE=merman` | `MARK_D2_ENGINE=resvg` |
| `mark.toml`<br>Linux: `~/.config/mark.toml`<br>macOS: `~/Library/Application Support/mark.toml`<br>Windows: `%AppData%\mark.toml` | `mermaid-engine = "merman"` | `d2-engine = "resvg"` |

```bash
mark --mermaid-engine=merman --d2-engine=resvg -f page.md
```

The font directory is set the same three ways:

| Way | Font directory |
|---|---|
| Flag, per run | `--d2-font-dir ~/.local/share/fonts/d2` |
| Env var | `MARK_FONT_DIR=~/.local/share/fonts/d2` |
| `mark.toml`<br>Linux: `~/.config/mark.toml`<br>macOS: `~/Library/Application Support/mark.toml`<br>Windows: `%AppData%\mark.toml` | `d2-font-dir = "/home/<you>/.local/share/fonts/d2"` |

Everything in `mark.toml`, e.g. on Linux:

```toml
mermaid-engine = "merman"
d2-engine      = "resvg"
d2-font-dir    = "/home/<you>/.local/share/fonts/d2"   # Windows: 'C:\Users\<you>\AppData\Local\mark\fonts'
```

### 4.4 Docker

Not published; build locally.

| Image | Dockerfile | Contents |
|---|---|---|
| Chrome | `Dockerfile` | mark + headless Chrome |
| Native | `Dockerfile.nobrowser` | mark + merman-cli + resvg + d2 fonts, amd64 only, no maths |

```bash
docker build -t mark .                                  # Chrome
docker build -t mark:nobrowser -f Dockerfile.nobrowser . # native
docker run --rm -e MARK_PASSWORD -v "$PWD:/docs" mark mark -f page.md
```

---

## 5. Configuration

### 5.1 Get an Atlassian API token

1. Open <https://id.atlassian.com/manage-profile/security/api-tokens> (logged in with your Atlassian account).
2. **Create API token** (the classic one, without scopes).
3. Name it (e.g. `mark`), choose an expiry, **Create**.
4. **Copy** it now: it is shown only once.

The token acts as your password: mark logs in with your email + the token.

### 5.2 Create `mark.toml`

| OS | Path |
|---|---|
| Linux | `~/.config/mark.toml` |
| macOS | `~/Library/Application Support/mark.toml` |
| Windows | `%AppData%\mark.toml` |
| any, elsewhere | `--config <path>` or `MARK_CONFIG=<path>` |

```toml
username = "you@example.com"                  # Atlassian account email
password = "<API token>"
base-url = "https://<org>.atlassian.net/wiki"
```

Linux / macOS, make it readable only by you:

```bash
chmod 600 ~/.config/mark.toml   # macOS: ~/Library/Application\ Support/mark.toml
```

The same file also takes the engines (§4.3, step 4) and every other flag, under the flag's name.

### 5.3 Token via env var instead (optional)

To keep the token out of the file, drop `password` from it and export:

```bash
export MARK_PASSWORD=<API token>
```

It wins over the file. Needed for Docker (`docker run -e MARK_PASSWORD ...`) and CI.

### 5.4 Check

```bash
mark --dry-run --log-level DEBUG -f some-page.md
```

A publish that changes nothing:

- `-f some-page.md`: the file to process.
- `--dry-run`: reads Confluence to resolve space, parents and folders, renders the page, then stops. Nothing is created, uploaded or updated; missing ancestors are simulated.
- `--log-level DEBUG`: prints how each ancestor was resolved and the rendered HTML (`TRACE` adds the HTTP requests, token redacted).

If it succeeds, credentials, `base-url`, space access and the installed renderers all work.

---

## 6. License

Apache 2.0, as upstream. See [LICENSE](./LICENSE).
