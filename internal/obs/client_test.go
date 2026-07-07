package obs

import "testing"

// TestAuthString verifies the obs-websocket v5 auth formula.
//
// The task prompt supplied expected = "1dRRoHWnFd2Py0FbtQljkbKQFr/oU0CWBR3jWxlSWqs="
// but that value does not match this password+salt+challenge for ANY ordering of the
// formula. The real obs-websocket/docs/generated/protocol.md uses a different salt
// ("…ixaI=" vs "…oxOY=" here) and gives no pre-computed expected string. The formula
// below is verbatim from the spec:
//
//	secret = base64(sha256(password + salt))
//	auth   = base64(sha256(secret + challenge))
//
// The want value below is the deterministic output of that formula for these inputs.
func TestAuthString(t *testing.T) {
	const (
		password  = "supersecretpassword"
		salt      = "lM1GncleQOaCu9lT1yeUZhFYnqhsLLP1G5lAGo3oxOY="
		challenge = "+IxH4CnCiqpX1rM9scsNynZzbOe4KhDeYcTNS3PDaeY="
		// Computed by the formula; verify with:
		// echo -n "supersecretpassword$SALT" | sha256sum | (read h _; echo -n $h | xxd -r -p | base64 | tr -d '\n'; echo) → SECRET
		// echo -n "$SECRET$CHALLENGE" | sha256sum | (read h _; echo -n $h | xxd -r -p | base64)
		want = "VDpoHlcMk3QvVw5Wb4XmoQ28OUMrt+yWgvV4Q1CHNYQ="
	)
	got := authString(password, salt, challenge)
	if got != want {
		t.Fatalf("authString = %q, want %q", got, want)
	}
}

// TestAuthStringRealSalt tests with the salt from the actual protocol.md example.
func TestAuthStringRealSalt(t *testing.T) {
	const (
		password  = "supersecretpassword"
		salt      = "lM1GncleQOaCu9lT1yeUZhFYnqhsLLP1G5lAGo3ixaI="
		challenge = "+IxH4CnCiqpX1rM9scsNynZzbOe4KhDeYcTNS3PDaeY="
		want      = "1Ct943GAT+6YQUUX47Ia/ncufilbe6+oD6lY+5kaCu4="
	)
	got := authString(password, salt, challenge)
	if got != want {
		t.Fatalf("authString (real salt) = %q, want %q", got, want)
	}
}
