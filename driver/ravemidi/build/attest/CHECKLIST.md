# Partner Center attestation submission checklist (ravemidi)

Full decision doc + one-time setup (EV cert, registration): `.devnotes/DRIVER_TRUST_PLAN.md`.

## One-time (account)

- [ ] EV code-signing cert purchased (Certum / SSL.com / Sectigo / DigiCert...; key on FIPS token)
- [ ] Hardware Program registered (Entra global admin, D-U-N-S/company info, legal-contact email answered, questionnaire done)
- [ ] EV cert uploaded + verified under Partner Center > Manage certificates
- [ ] (API only) Entra app associated with Partner Center, role **Hardware / Driver Submitter**; tenant/client id + secret stored OUTSIDE the repo

## Per release

- [ ] `gh workflow run driver.yml --ref <branch>` -> download artifact `ravemidi-attestation-cab-unsigned`
- [ ] Inspect: `disk1/ravemidi.cab` contains `ravemidi\ravemidi.inf|.sys|.pdb` (subfolder, no root files); hashes match `manifest.txt`
- [ ] `..\sign-cab.ps1 -CabPath disk1\ravemidi.cab` on the token machine (signtool sign /fd sha256 + RFC3161 timestamp)
- [ ] Submit: dashboard (Submit new hardware; test-signing options UNCHECKED; request Win10/11 x64) OR `attest-submit.ps1`
- [ ] Wait ~10min-1h; commitStatus CommitComplete; download signed package
- [ ] Verify signed package: `signtool verify /kp /v ravemidi.sys`; MS-signed `.cat` present (MS regenerates it - discard ours)
- [ ] Clean Secure-Boot-ON VM: `pnputil /add-driver ravemidi.inf /install` + `devgen /add /bus ROOT /hardwareid Root\ravemidi` loads without warnings
- [ ] Attach signed package to the GitHub release / installer feed

## Gotchas (from MS docs)

- Driver folder in the cab: same arch set per folder, name < 40 chars, no special chars, no UNC paths.
- .pdb REQUIRED (MS automated crash analysis).
- Attestation = Win10/11 client only. NO Windows Server (>=2016 rejects attested drivers), NO Windows Update retail publishing, not "Windows Certified".
- Keep Partner Center identity verification current - re-verification sweeps can lock accounts (~60d appeals).
