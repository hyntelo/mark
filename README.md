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

mark refuses a merman older than the version in [`mermaid/merman-version.txt`](./mermaid/merman-version.txt).
The commands below read that version from the file, so they always install the right one.

Linux x86_64, macOS (Intel and Apple Silicon):

```bash
V=$(curl -fsSL https://raw.githubusercontent.com/hyntelo/mark/master/mermaid/merman-version.txt)
case "$(uname -s)-$(uname -m)" in
  Linux-x86_64)  T=x86_64-unknown-linux-gnu ;;
  Darwin-x86_64) T=x86_64-apple-darwin ;;
  Darwin-arm64)  T=aarch64-apple-darwin ;;
  *) echo "no prebuilt merman-cli for $(uname -s)-$(uname -m): see 'Other platforms' below" ;;
esac
cd "$(mktemp -d)"
curl -fsSLO "https://github.com/Latias94/merman/releases/download/v$V/merman-cli-$T.tar.xz"
tar -xJf "merman-cli-$T.tar.xz"
sudo install -m 0755 "merman-cli-$T/merman-cli" /usr/local/bin/merman-cli
merman-cli --version
```

Windows x86_64 (PowerShell):

```powershell
$V = (Invoke-RestMethod https://raw.githubusercontent.com/hyntelo/mark/master/mermaid/merman-version.txt).Trim()
$D = "$env:LOCALAPPDATA\mark"; New-Item -ItemType Directory -Force $D | Out-Null
$Z = "$env:TEMP\merman-cli.zip"
Invoke-WebRequest "https://github.com/Latias94/merman/releases/download/v$V/merman-cli-x86_64-pc-windows-msvc.zip" -OutFile $Z
Expand-Archive -Force $Z "$env:TEMP\merman-cli"
Copy-Item -Force (Get-ChildItem -Recurse "$env:TEMP\merman-cli" -Filter merman-cli.exe).FullName $D
merman-cli --version   # $D is on PATH from §4.1
```

Other platforms (Linux arm64, Windows arm64): build it with [Rust](https://rustup.rs).
`--version` is required: without it cargo installs the latest *stable* release,
which is older than mark accepts.

```bash
V=$(curl -fsSL https://raw.githubusercontent.com/hyntelo/mark/master/mermaid/merman-version.txt)
cargo install --locked --force merman-cli --version "$V"
merman-cli --version
```

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

**Step 1 — generate it with placeholders.** An existing file is never overwritten.

Linux / macOS:

```bash
case "$(uname)" in Darwin) C="$HOME/Library/Application Support/mark.toml" ;; *) C="$HOME/.config/mark.toml" ;; esac
if [ -e "$C" ]; then echo "$C already exists, not touched"; else
mkdir -p "$(dirname "$C")"
cat > "$C" <<'EOF'
username = "<your-email>"
password = "<api-token>"
base-url = "https://<your-org>.atlassian.net/wiki"

# Page title and body
title-from-h1 = true
drop-h1       = true
features      = ["d2", "mermaid", "mention", "mkdocsadmonitions"]
layout        = "article"

# Publishing
edit-lock    = true
changes-only = true

# Diagrams
mermaid-scale  = 3
d2-scale       = 10
mermaid-config = "<absolute-path-to>/mermaid-config.json"

# Native renderers (§4.3). Delete these three lines to use Chrome.
mermaid-engine = "merman"
d2-engine      = "resvg"
d2-font-dir    = "<absolute-path-to-d2-fonts>"
EOF
chmod 600 "$C"; echo "created $C"
fi
```

Windows (PowerShell):

```powershell
$C = "$env:APPDATA\mark.toml"
if (Test-Path $C) { "$C already exists, not touched" } else {
@'
username = "<your-email>"
password = "<api-token>"
base-url = "https://<your-org>.atlassian.net/wiki"

# Page title and body
title-from-h1 = true
drop-h1       = true
features      = ["d2", "mermaid", "mention", "mkdocsadmonitions"]
layout        = "article"

# Publishing
edit-lock    = true
changes-only = true

# Diagrams
mermaid-scale  = 3
d2-scale       = 10
mermaid-config = "<absolute-path-to>/mermaid-config.json"

# Native renderers (§4.3). Delete these three lines to use Chrome.
mermaid-engine = "merman"
d2-engine      = "resvg"
d2-font-dir    = "<absolute-path-to-d2-fonts>"
'@ | Set-Content -Encoding utf8 $C
"created $C"
}
```

**Step 2 — replace the placeholders.**

| Key | Placeholder | Put | Watch out |
|---|---|---|---|
| `username` | `<your-email>` | your Atlassian account email | |
| `password` | `<api-token>` | the token from §5.1 | **secret**: this file only, never in a repo, a script or a chat. Or delete the line and use §5.3 |
| `base-url` | `<your-org>` | your Confluence Cloud site (`https://<org>.atlassian.net/wiki`) | internal: do not write the real one in this public repo |
| `mermaid-config` | `<absolute-path-to>` | absolute path of the vault's `mermaid-config.json` | absolute (TOML does not expand `~`); names your vault, so keep it out of this repo. Delete the line if you have none |
| `d2-font-dir` | `<absolute-path-to-d2-fonts>` | the folder of §4.3 step 3, e.g. `/home/<you>/.local/share/fonts/d2` | absolute; only with `d2-engine = "resvg"` |
| `mermaid-engine`, `d2-engine`, `d2-font-dir` | — | keep for the native renderers, delete for Chrome | |

The other keys are the vault's publishing settings; keep them identical to the CI's,
or a page published from your machine comes out different from the same page
published by the pipeline:

| Key | Effect |
|---|---|
| `title-from-h1` | page title = the document's `# H1` (vault files have no `Title` header) |
| `drop-h1` | removes that H1 from the body, so the title is not shown twice |
| `features` | enabled syntaxes; **replaces** the default (`mermaid`, `mention`) |
| `layout` | `article`: narrow, centred reading width |
| `edit-lock` | marks the page as generated, discourages editing it in Confluence |
| `changes-only` | skips the update when the body did not change (no empty versions) |
| `mermaid-scale`, `d2-scale` | PNG sharpness |

Every other flag can go in this file too, under the flag's name.

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
