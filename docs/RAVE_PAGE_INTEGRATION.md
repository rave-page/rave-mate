# rave-mate ↔ rave.page deploy integration

How the rave-mate desktop companion is published and what the **rave.page web repo**
needs from its FE dev. rave-mate is a separate GitLab project (`the-box.io/rave-mate-go`)
but deploys to the **same** per-branch installer feed as the Electron client.

## How it publishes (rave-mate side - already done)

`rave-mate/.gitlab-ci.yml` `deploy` job rsyncs into a `mate/` **subdir** of the existing
Electron feed on the Docker host (<deploy-host>):

```
${INSTALLER_FEED_ROOT}/app_${branch}/mate/
  rave-mate-<build>-<date>-<commit>.exe        # versioned raw exe - the updater's target
  rave-mate-setup-<build>-<date>-<commit>.exe  # versioned NSIS installer (web download)
  rave-mate-linux-<build>-<date>-<commit>      # versioned Linux build
  rave-mate.exe / rave-mate-setup.exe / rave-mate-linux  # stable fallback copies
  latest.json (+ .sig)                         # auto-update manifest (polled by the updater)
```

**Why versioned filenames:** the CDN/nginx caches a *fixed* name (`rave-mate.exe`) and keeps
serving the old blob after a new deploy → the in-app updater downloads stale bytes whose hash
mismatches the fresh manifest (`checksum mismatch: got … want …`). A versioned name was never
cached, so it can't be a stale hit. The manifest points the updater + web downloads at the
versioned URLs; stable copies remain only as a fallback.

The app container already bind-mounts `app_${branch}/` at `/usr/share/nginx/html/app/`
(read-only) and its nginx serves it at `/app/`. The `mate/` subdir therefore serves at:

```
https://${branch}.rave.page/app/mate/rave-mate-setup-<build>-<date>-<commit>.exe
https://${branch}.rave.page/app/mate/latest.json
master → https://rave.page/app/mate/...
```

**No Ansible/nginx change required** - it's the same mount the Electron feed uses.

## `latest.json` schema

```json
{
  "version": "development-abc1234",
  "build": 4242,
  "commit": "abc1234",
  "url": "https://development.rave.page/app/mate/rave-mate-4242-20260604-abc1234.exe",
  "sha256": "<hex of that versioned exe>",
  "installer_url": "https://development.rave.page/app/mate/rave-mate-setup-4242-20260604-abc1234.exe",
  "installer_sha256": "<hex of the installer>",
  "linux_url": "https://development.rave.page/app/mate/rave-mate-linux-4242-20260604-abc1234",
  "linux_sha256": "<hex of the linux binary>",
  "released": "2026-06-04T18:30:00Z",
  "notes": "<commit title>"
}
```

`build` is the GitLab pipeline id (monotonic) - the in-app updater treats a higher `build`
as "newer". `url`/`sha256` are the **versioned raw exe** the updater self-applies (download →
sha256 verify → swap → relaunch). `installer_url`/`linux_url` are for the web download page
(clean NSIS installer / Linux binary); the updater ignores them. All URLs are versioned so a
CDN can't serve them stale.

## Required CI/CD variables (rave-mate-go project)

Set on `the-box.io/rave-mate-go` (group-level inherited is fine):

| Variable | Value | Notes |
|---|---|---|
| `SSH_PRIVATE_KEY` | deploy key for `deploy@<deploy-host>` | same key the web repo uses; **Protected + Masked** |
| `SSH_KNOWN_HOSTS` | output of `ssh-keyscan <deploy-host>` | **recommended** - pins the host key so the deploy isn't TOFU. If unset, CI falls back to `ssh-keyscan` + `accept-new` (never disables checking). |
| `DEPLOY_HOST` | `<deploy-host>` | defaulted in CI; override only if it moves |
| `DEPLOY_USER` | `deploy` | defaulted |
| `INSTALLER_FEED_ROOT` | `/home/deploy/ravepage/installers` | defaulted |
| `UPDATE_SIGNING_KEY_PEM` | Ed25519 **private** key (PEM) | **recommended** - signs `latest.json`. **Protected + Masked**, set on the protected branches only. |
| `UPDATE_PUBKEY` | base64 raw 32-byte Ed25519 **public** key | embedded in the binaries; when present the updater **requires** a valid signature. |
| `MATE_FEED_KEEP` | integer (default `10`) | how many recent builds to retain per family; the deploy job prunes older versioned artefacts (`scripts/prune-feed.sh`) so the feed doesn't grow unbounded. |

> **Auto-update is Windows-only.** The manifest's `sha256`/`url` are the Windows exe's, so
> the in-app updater self-applies on Windows only. `rave-mate-linux` is published for manual
> download; a Linux auto-update would need per-platform checksums in the manifest.

### Update signing (Ed25519) - generate the keypair once

The updater verifies a detached Ed25519 signature over `latest.json` against a public key
baked into the binary, so a feed-write attacker can't forge a release without the private
key. Until both vars are set, CI publishes an **unsigned** manifest and the updater falls
back to sha256 + same-origin (still safe against tampering-in-transit, not against a feed
compromise). To provision:

```sh
# 1. private key (→ UPDATE_SIGNING_KEY_PEM, keep secret)
openssl genpkey -algorithm ed25519 -out rave-mate-update.pem
cat rave-mate-update.pem            # paste into UPDATE_SIGNING_KEY_PEM (Protected+Masked)

# 2. matching public key, raw 32 bytes, base64 (→ UPDATE_PUBKEY)
openssl pkey -in rave-mate-update.pem -pubout -outform DER | tail -c 32 | base64 -w0
```

Set both on `the-box.io/rave-mate-go`. The next pipeline embeds `UPDATE_PUBKEY` in every
binary and signs each `latest.json` → `latest.json.sig`. Rotating the key requires shipping
a new binary with the new `UPDATE_PUBKEY` (old installs trust only the key they were built
with) - so rotate by releasing, not by swapping the feed.

The deploy job runs only on `development`, `testing`, `staging`, `master`.

## Action items for the rave.page FE dev

### 1. (Recommended) Explicitly protect `mate/` from the Electron rsync

The Electron job's rsync already protects subdirs (its `--exclude='*'` keeps `--delete-after`
from descending into `mate/`), so today it's safe. To make it robust against future filter
edits, add an explicit protect rule in `.gitlab-ci.yml` `build:electron`, just before the
`--include` lines:

```diff
       rsync -avz --mkpath --delete-after
+      --filter='protect /mate/'
       --include='*.exe'
       --include='*.AppImage'
```

`protect /mate/` guarantees the rave-mate subdir is never deleted regardless of the include
list.

### 2. Surface the download in the web app

rave-mate is a normal static download - no app logic needed beyond a link. Suggested:

- On the existing desktop-download surface (where the Electron client is offered), add a
  **"rave-mate (native companion)"** card.
- Link: `\`${WEBSITE_URL}/app/mate/rave-mate.exe\`` (Windows) and `.../rave-mate-linux`.
- Optional version display: `fetch('/app/mate/latest.json')` → show `version` / `released`.
  The file is small and `Cache-Control: no-cache`-friendly; treat a 404 as "not yet built
  for this branch".

Example (mirror whatever the Electron download card does):

```ts
const feed = `${import.meta.env.VITE_WEBSITE_URL}/app/mate`;
const meta = await fetch(`${feed}/latest.json`).then(r => r.ok ? r.json() : null);
// download href = `${feed}/rave-mate.exe`; label with meta?.version
```

That's the whole web-side surface - the desktop app handles its own updates from
`latest.json` after first install.
