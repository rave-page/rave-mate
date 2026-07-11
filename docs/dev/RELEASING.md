# Releasing

## Channels

Every build carries a channel in `internal/version` (`Channel`, ldflags-stamped, else derived
from the `Version` branch prefix):

| Channel | Source | Behavior |
|---|---|---|
| `production` | `master`/`main` builds, tags without suffix | no warning |
| `beta` | `staging` builds, `v*-beta.*` tags | launch warning |
| `alpha` | any other branch (e.g. `development`), `v*-alpha.*` tags | launch warning |
| `nightly` | `nightly.yml` rolling build off `development` tip | launch warning, self-updates from the `nightly` release feed |
| `dev` | unstamped local build | launch warning, self-updater off |

**We are in ALPHA.** Only `nightly` and `alpha` builds are cut right now - there is no
`beta`/`production` release yet. During the alpha phase `release.yml` maps a plain `v*` tag to
alpha (not production); flip that default when leaving alpha.

**Non-production builds warn on every launch**: development release, keep backups of media/
library/config, authors not liable for file/system damage (LICENSE §15–16). Don't remove this.

## GitHub releases (standalone repo)

GitHub Releases are the distribution + self-update feed. Both workflows cross-build the
full-feature Windows exe on Linux via mingw (`-tags "spout vr abletonlink"`, SDKs fetched
SHA-pinned by `scripts/fetch-spout.sh` / `fetch-link.sh`), the Linux binary, and the NSIS
installer, then publish the complete feed set to a release:

- versioned raw exe `rave-mate-<build>-<date>-<commit>.exe` (updater target; immutable name
  defeats stale CDN caches) + stable `rave-mate.exe`
- versioned + stable NSIS installer, versioned + stable Linux binary
- `SpoutLibrary.dll`, `openvr_api.dll` (stable names - `assets[]` in the manifest AND the
  vrdll/spoutdll self-heal buttons fetch `<feed>/<dll>` directly)
- `latest.json` (+ `latest.json.sig` when signing is provisioned)

**Nightly (dev channel):** `nightly.yml` on every push to `development` (and daily) rolls the
single **`nightly`** prerelease (delete+recreate tag). Feed baked into the binary:
`https://github.com/rave-page/rave-mate/releases/download/nightly/`.

**Tagged releases** - push a tag (alpha phase → alpha only):
- `v1.4.0-beta.1` → beta (post-alpha)
- anything else (`v1.4.0`, `v1.4.0-alpha.3`) → alpha (flip in `release.yml`'s channel step
  when leaving alpha)

`release.yml` publishes TWO releases per tag: the immutable versioned release (the tag, for
humans/history) and the rolling **channel** release (`alpha`/`beta` tag, delete+recreate) the
updater feed points at. `workflow_dispatch` re-rolls the channel release from the current ref
without a tag.

**Feed manifest (`latest.json`):** built with `jq` in the publish job - schema is
`internal/shared/selfupdate.Release`. `build` = `GITHUB_RUN_NUMBER` (monotonic per workflow =
per channel; the updater compares build numbers, not semver).

**Browser CORS mirror:** release-asset downloads send NO CORS headers, so the rave.page
download page can't fetch `latest.json` from the release. The publish jobs mirror it to the
orphan **`feeds` branch** as `<channel>.json` - browsers read
`https://raw.githubusercontent.com/rave-page/rave-mate/feeds/<channel>.json` (ACAO:*).

**vrdll lockstep gate:** the build fails if `internal/vroverlay/sdk/openvr_api.dll`'s sha256
differs from `internal/vrdll.dllSHA` - the in-app "install VR runtime" button pins that hash,
so shipping mismatched bytes would brick the self-heal download. Bump both together.

CI (`ci.yml`) + security analysis (`security.yml`: CodeQL, govulncheck, supply-chain soak)
run on every push/PR.

## Secrets + signing

| Secret | Purpose | If absent |
|---|---|---|
| `UPDATE_SIGNING_KEY_PEM` | Ed25519 private key (PKCS8 PEM). Publish jobs sign the exact `latest.json` bytes (`openssl pkeyutl -rawin`, base64 raw 64-byte sig → `latest.json.sig`). | Skipped cleanly (forks/PRs build green): builds stamp an EMPTY `UpdatePubKey` override so the binary keeps the designed sha256 + same-origin fallback, and the feed publishes unsigned. |
| `CODE_SIGN_PFX_B64` | base64 PKCS12 Authenticode cert (currently self-signed, `CN=rave-mate, O=Magnifica UG`). `osslsigncode` signs `rave-mate.exe` BEFORE NSIS packs it, then the installer - so the manifest sha256s cover the signed bytes. | Skipped cleanly - unsigned binaries (SmartScreen warns either way with a self-signed cert). |
| `CODE_SIGN_PFX_PASS` | PFX password (passed via `-readpass` file, never argv). | With the cert: signing fails. |

All three secrets ARE set on `rave-page/rave-mate`, so official builds always ship a signed
feed + Authenticode-signed exe/installer in practice; the skips exist for forks. With the key
present, the build preflight asserts it derives exactly the `UpdatePubKey` embedded in
`internal/version/version.go` - rotate the key and that constant in lockstep. Publish jobs also
create **SLSA build-provenance attestations** (`actions/attest-build-provenance`) for the
versioned exe/installer/linux binary + `latest.json`; verify any downloaded artifact with
`gh attestation verify <file> -R rave-page/rave-mate`. Never echo or commit key material.

## Self-updater

Builds stamped with `FeedURL` poll their own feed; when `UpdatePubKey` is stamped the manifest
MUST be Ed25519-signed. The updater swaps ONLY the exe - runtime-loaded DLLs
(`openvr_api.dll`, `SpoutLibrary.dll`) must be listed as manifest assets or self-heal from the
Settings page. GitHub feeds get one carve-out: asset downloads may 302 to
`https://*.githubusercontent.com` (sha256 still gates every byte). Existing installs fed from
`*.rave.page/app/mate/` keep polling that GitLab-published feed - they migrate to the GitHub
feed only via one more GitLab-feed update whose binary bakes the new `FeedURL`.

### In-app update flow (`internal/updater` + webview surfaces)

`internal/updater.Manager` wraps `shared/selfupdate` in an explicit state machine:
`idle → available → downloading → downloaded(verified) → staged(needs-restart)`. It polls
every 5 min (first check 30 s after launch; failures back off doubling to 80 min, logged via
`logbus.Gate` so an offline box never spams). First detection of a version notifies once -
tray balloon + toast - persisted in `config.UpdateNotifiedFor`. Three surfaces render from the
one Manager: the nav-rail bottom block (`#nav-update`, one state-dependent action button,
nothing when up to date), the tray menu's dynamic item ("Download update X" / "Install
update" / "Restart to finish update"), and Settings → System → Updates. `selfupdate.Apply` is
split into `Download` (stage + verify next to the exe, exe untouched) and `Staged.Install`
(atomic rename swap); one-shot callers (ctl `SELF-UPDATE`, peer remote update) still use
`Apply`. Signature/checksum failures surface in the UI verbatim and never advance the machine.

## Exporting rave-mate from the rave-suite monorepo → GitHub

The upstream home is the private rave-suite monorepo (GitLab); the GitHub repo is the exported
`rave-mate/` directory. Export checklist:

1. `git archive HEAD:rave-mate` (plain snapshot, no history) or `git subtree split`/
   `git filter-repo` on `rave-mate/` if history is wanted. rave-mate is a **self-contained
   module** - shared code is folded into `internal/shared`, so there is NO shared-module
   vendoring step and no go.mod `replace` to apply.
2. Verify no monorepo-only files leaked: local agent memory, `CLAUDE.local.md`, credentials,
   internal host/IP docs. `docs/RAVE_PAGE_INTEGRATION.md` stays sanitized (placeholders, no
   internal IPs). Gitignored local-only build inputs (`third_party/{spout,link}` SDKs, `dist/`,
   `*.exe`) are excluded by `git archive` automatically.
3. `go build ./... && go test ./...` with `GOWORK=off` in the export (proves self-contained).
4. Push; confirm the three workflow badges in README go green; enable GitHub private
   vulnerability reporting + Dependabot alerts in repo settings.
5. README badge URLs assume `github.com/rave-page/rave-mate` - adjust if the org/name differs.

GitLab (rave-suite) remains the deploy pipeline for rave.page infrastructure; GitHub is the
public source + community release channel.
