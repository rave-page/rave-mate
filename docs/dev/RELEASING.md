# Releasing

## Channels

Every build carries a channel in `internal/version` (`Channel`, ldflags-stamped, else derived
from the `Version` branch prefix):

| Channel | Source | Behavior |
|---|---|---|
| `production` | `master`/`main` builds, tags without suffix | no warning |
| `beta` | `staging` builds, `v*-beta.*` tags | launch warning |
| `alpha` | any other branch (e.g. `development`), `v*-alpha.*` tags | launch warning |
| `nightly` | `nightly.yml` rolling build off `development` tip | launch warning, updater off |
| `dev` | unstamped local build | launch warning, self-updater off |

**We are in ALPHA.** Only `nightly` and `alpha` builds are cut right now - there is no
`beta`/`production` release yet. During the alpha phase `release.yml` maps a plain `v*` tag to
alpha (not production); flip that default when leaving alpha.

**Non-production builds warn on every launch**: development release, keep backups of media/
library/config, authors not liable for file/system damage (LICENSE §7–8). Don't remove this.

## GitHub releases (standalone repo)

**Nightly (dev channel):** `.github/workflows/nightly.yml` rebuilds Windows + Linux on every
push to `development` (and daily), stamping `Channel=nightly`, and rolls a single **`nightly`**
prerelease (tag + release re-pointed to the newest commit) so anyone can download the latest dev
build from the Releases page. No tag push needed.

**Tagged releases** - push a tag (alpha phase → alpha only):
- `v1.4.0-alpha.3` → alpha prerelease
- `v1.4.0-beta.1` → beta prerelease (post-alpha)
- `v1.4.0` → alpha prerelease for now (production once we leave alpha)

`.github/workflows/release.yml` builds Windows + Linux, stamps
`version.{Version,Commit,Build,Channel}`, and attaches artifacts to a GitHub Release
(prerelease-flagged during alpha). CI (`ci.yml`) + security analysis (`security.yml`:
CodeQL, govulncheck, supply-chain soak) run on every push/PR.

## Self-updater

Builds stamped with `FeedURL` poll their own feed; when `UpdatePubKey` is stamped the manifest
MUST be Ed25519-signed. The updater swaps ONLY the exe - runtime-loaded DLLs
(`openvr_api.dll`, `SpoutLibrary.dll`) must be listed as manifest assets or self-heal from the
Settings page.

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
