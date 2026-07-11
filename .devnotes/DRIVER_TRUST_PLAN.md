# ravemidi driver trust plan — attestation now, WHCP later

Status: DECISION DOC. Research 2026-07-11 (MS Learn docs current as of 2026-03/04 updates).
Goal: end users install ravemidi WITHOUT test-signing mode / Secure Boot off.
Companion: `RAVEMIDI_DRIVER_DESIGN.md` §Signing (2026-07-10 research pass — still accurate).

## Verdict

**Attestation signing first.** Free per-submission, ~10min–1h turnaround, covers exactly our
audience (Win10/11 client x64; arm64 later with NTARM64 INF sections). Distribution via our
own installer/GitHub releases — same model as WireGuard/WinFsp/Dokany.
**WHCP/HLK later, only if** Windows Update distribution or Windows Server support ever
matters (they don't for DJ software).

Why the .sys can't be EV-signed directly: Secure Boot Win10 1607+ loads only
Microsoft-signed kernel code; cross-signing trust fully removed Apr 2026. The MS attestation
signature is the load gate — our EV cert only authenticates the *submission cab*.

## What each path yields (confirmed against MS Learn, 2026-03/04 revisions)

| | Attestation | WHCP (HLK-tested) |
|---|---|---|
| Loads on retail Win10/11 client (Secure Boot on) | YES | YES |
| Windows Server ≥2016 | NO (rejected outright) | YES |
| Windows Update retail publishing | NO (CoDev/Test-key test audiences only) | YES |
| Vista/7/8.x | NO | YES |
| "Windows Certified" claim / logo | NO | YES |
| DUA (driver update acceptable) fast-path | NO | YES |
| Cost | free per submission | HLK lab time (heavy) |
| Turnaround | ~10min–1h | days–weeks per OS release |

⚠ **needs-confirmation (policy drift risk)**: the 2026-03 revision of *Driver Signing
Options* retitled the section "Attestation signed drivers **for testing scenarios**" and the
attestation how-to now opens "For testing purposes only…". The enumerated technical
restriction is still only *WU retail publishing*; attestation-signed drivers continue to
load on retail machines and vendor-installer distribution remains the industry norm
(WireGuard, WinFsp, every niche vendor). But Microsoft is visibly tightening framing —
re-read `driver-signing-offerings` before each release cycle; if MS ever gates attestation
loads to provisioned machines, WHCP becomes mandatory.

## USER ACTION LIST (one-time, ~1–2 weeks wall clock, mostly waiting)

Order matters: EV cert BEFORE Partner Center registration (registration requires uploading it).

### 1. Buy an EV code-signing certificate

Required to *register* the Hardware Program + to sign submission cabs. Ordinary Authenticode
registered to the account is technically allowed per-submission, but EV is required at
registration and recommended throughout — keep it simple, use EV for both.
**Azure Trusted Signing is NOT accepted** (not on MS's CA list; no kernel-mode EKU).

MS-listed CAs (code-signing-reqs, 2026-04): Certum, DigiCert, GlobalSign, IdenTrust,
Sectigo, SSL.com. Current vendor picks (prices ≈ mid-2026 list, needs-confirmation at
purchase time):

| CA | Price ≈/yr | Notes |
|---|---|---|
| **Certum** (PL) | ~€300–380 | EU vendor, popular with open-source/indie devs; ships card+reader or works with own token |
| **SSL.com** | ~$349 | sole-proprietor-friendly EV tier; USB token (+shipping, expedited ~$599 extra) or eSigner cloud HSM (cloud variant fine for the cab — kernel trust comes from MS, not this cert) |
| **Sectigo** | ~$279–499 (resellers cheaper) | widely resold; token shipped |
| DigiCert | ~$560–700 | premium price, fast support |

Hard constraints (CA/B Forum): private key must live on FIPS 140-2 L2 hardware (USB
token/HSM — no soft PFX, since 2023); max cert lifetime ~460 days ≈ 15 months (since
2026-02) so budget annual renewal. EV = business validation: registered business or
sole proprietorship, verifiable phone + registry/D-U-N-S entry; expect 1–7 business days
validation + token shipping.

### 2. Register Microsoft Partner Center Hardware Program (free)

https://partner.microsoft.com/dashboard/account/exp/enrollment/welcome?accountProgram=hardware

- Sign in as **Microsoft Entra ID global administrator** of an org tenant (create a free
  tenant during registration if none).
- Provide company info or D-U-N-S; legal contact must answer MS's verification email;
  complete the follow-up questionnaire (registration stalls until answered).
- Upload the EV cert under **Manage certificates** (sign the DefaultTestBinary they provide).
- Accept: Code Signing Agreement, Windows Hardware Compatibility Agreement, MMLA,
  Windows Analytics Agreement.
- Duration: days (identity checks) — the EV validation in step 1 is the long pole.

⚠ Keep account verification CURRENT: Partner Center identity re-verification sweeps
(2025-10+) have locked out WireGuard/VeraCrypt-class accounts with ~60-day appeals. Never
gate a release on a same-week Partner Center action.

### 3. Per-release loop (user, ~15 min hands-on)

1. Run the `attestation-package` CI job (workflow_dispatch on `driver.yml`) → download
   artifact `ravemidi-attestation-cab-unsigned`.
2. On the machine with the EV token: `driver/ravemidi/build/sign-cab.ps1 -CabPath disk1\ravemidi.cab`
   (thin wrapper over `signtool sign /fd sha256 /td sha256 /tr <CA timestamp>`).
3. Partner Center → Submit new hardware → upload signed cab → leave test-signing options
   UNchecked → request Win10/11 x64 signatures → Submit. (Or script it:
   `driver/ravemidi/build/attest/attest-submit.ps1`, Hardware Dashboard API.)
4. ~10min–1h: download signed package (MS-embedded SHA-2 sig in .sys + MS-signed .cat) →
   attach to GitHub release / fold into installer. MS regenerates the .cat; any .cat we
   ship pre-submission is discarded.
5. Verify on a clean Secure-Boot-ON VM: `pnputil /add-driver ravemidi.inf /install` +
   `signtool verify /kp /v ravemidi.sys`.

## What stays automated in CI (no secrets in CI, ever)

- `driver.yml` `build` job (existing): Release+Debug x64, `/warnaserror`, InfVerif `/h`
  (DCH gate — attestation rejects non-declarative INFs since Apr 2025).
- `driver.yml` `attestation-package` job (NEW, manual dispatch): Release x64 build →
  InfVerif /h → `MakeCab /f ravemidi.ddf` (inf+sys+pdb in `ravemidi\` subfolder — pdb
  required for MS crash analysis; no root-level files; folder <40 chars, no special chars)
  → uploads `ravemidi-attestation-cab-unsigned` (disk1/ravemidi.cab + loose package/ +
  manifest with hashes).
- EV signing stays LOCAL (token is hardware; CI never holds it). If eSigner/cloud HSM is
  chosen later, a CI signing step becomes possible — revisit then.
- Submission automation skeleton: `driver/ravemidi/build/attest/attest-submit.ps1` —
  Entra client-credentials → `https://manage.devcenter.microsoft.com/v2.0/my/hardware/products`
  create product → create submission (returns SAS) → blob upload → commit → poll
  commitStatus. Config via env only (`RVM_TENANT_ID`/`RVM_CLIENT_ID`/`RVM_CLIENT_SECRET`).
  Reference impl: microsoft/SDCM (Surface Dev Center Manager). Needs a Partner Center
  Entra app with the **Hardware / Driver Submitter** role before it can run.

## WHCP/HLK path (deferred — only for WU retail or Server)

- Register (already done for attestation) → build HLK lab: HLK **controller** + **client**
  machines per targeted OS release (Win11 25H2 HLK + matching WHCP playlist; Win10 22H2 =
  legacy HCK-era playlist). Virtual/root-enumerated device is testable but the client must
  have the driver installed and the DUT enumerated.
- Playlist: current *WHCP Compatibility Playlist* (aka.ms/HLKPlaylist). For ravemidi HLK
  Studio would project features ≈ `Device.DevFund.*` (fundamentals: PNP stress, sleep,
  Driver Verifier, DF reinstall…) + possibly `Device.Audio.*` (we're a MEDIA-class PortCls
  driver exposing WaveRT-less MIDI pins) — ⚠ needs-confirmation by actually loading the
  driver in HLK Studio and seeing the projected feature list; MIDI-only KS filters may map
  to fundamentals-only.
- Package results as `.hlkx`, submit via dashboard → yields WU distribution (shipping
  labels), Server signing, "Certified" claim.
- Cost: lab hardware/VM time + per-OS-release re-runs. Skip until there's a business reason.

## AGPL / forks note

Forks that modify ravemidi.sys cannot reuse our Microsoft signature — they re-sign under
their own Partner Center account or run test-signed. Documented in driver README. Consider
the Wintun pattern (permissively-granted prebuilt signed binary) later.

## Sources (fetched 2026-07-11)

- learn.microsoft.com/windows-hardware/drivers/install/windows-driver-signing-tutorial (2025-01 rev)
- learn.microsoft.com/windows-hardware/design/compatibility/whcp-certification-process
- learn.microsoft.com/windows-hardware/drivers/dashboard/driver-signing-offerings (2026-03 rev — "testing scenarios" framing)
- learn.microsoft.com/windows-hardware/drivers/dashboard/code-signing-attestation (2025-07 rev, updated 2026-04)
- learn.microsoft.com/windows-hardware/drivers/dashboard/hardware-program-register (2025-04 rev)
- learn.microsoft.com/windows-hardware/drivers/dashboard/code-signing-reqs (2025-05 rev, updated 2026-04 — CA list)
- learn.microsoft.com/windows-hardware/drivers/dashboard/dashboard-api + manage-product-submissions (API endpoints)
- github.com/Microsoft/SDCM (reference dashboard-API client)
- CA/B Forum code-signing BRs (FIPS token 2023-06; ~460-day max lifetime 2026-02) — via vendor pages, needs-confirmation at purchase
