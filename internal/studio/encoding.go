package studio

// Wire encoding/canonical-JSON live in internal/wirecrypto (shared with internal/peerlink).
// These unexported aliases keep the studio call sites + TS-parity test unchanged. The
// JWT-claims helpers below stay here - they're rave.page-token-specific, not used by peers.

import (
	"bytes"
	"encoding/json"

	"rave.page/mate/internal/wirecrypto"
)

var (
	encB64url            = wirecrypto.EncB64url
	decB64url            = wirecrypto.DecB64url
	canonicalJSON        = wirecrypto.CanonicalJSON
	canonicalJSONValue   = wirecrypto.CanonicalJSONValue
	marshalNoHTMLEscape  = wirecrypto.MarshalNoHTMLEscape
	constantTimeEqualStr = wirecrypto.ConstantTimeEqualStr
)

// decodeJwtClaims decodes a JWT payload segment without verifying (claims peek only).
func decodeJwtClaims(token string) map[string]any {
	parts := bytes.Split([]byte(token), []byte("."))
	if len(parts) != 3 {
		return nil
	}
	payload, err := decB64url(string(parts[1]))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return nil
	}
	return claims
}

// tokenBindId mirrors TS: the JWT `jti` when present, else the full token (which still
// changes on refresh/re-login). The channel key folds in both peers' bind ids.
func tokenBindId(token string) string {
	if c := decodeJwtClaims(token); c != nil {
		if jti, ok := c["jti"].(string); ok && jti != "" {
			return jti
		}
	}
	return token
}
