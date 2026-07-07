# Security policy

## Reporting

Please report vulnerabilities privately: **security@rave.page** (or GitHub private vulnerability
reporting on this repo). Don't open public issues for exploitable bugs. We aim to acknowledge
within 72 h.

## Scope notes for researchers

- Secrets (rave.page tokens, VRChat session cookies, Twitch/GitHub tokens) are sealed at rest
  via OS facilities (Windows DPAPI through `internal/shared/secureseal`) and must never appear
  in logs. Log-leak reports are in scope.
- The local control socket (`127.0.0.1:47620`) and Studio WS channel (`127.0.0.1:47615-47619`,
  ECDH + per-frame HMAC + origin allowlist) are loopback-only by design; anything that makes
  them reachable off-host is in scope.
- The LAN peer link uses Ed25519-signed ECDH with SAS pairing; downgrade/MitM findings in scope.
- The self-updater requires an Ed25519-signed manifest when a public key is stamped; unsigned
  fallback is sha256 + same-origin. Bypass findings in scope.

## Supply chain

Dependencies are pinned exact versions with a 7-day minimum release age (`SUPPLY_CHAIN.md`,
`scripts/check-release-age.sh`), scanned by govulncheck + CodeQL in CI.
