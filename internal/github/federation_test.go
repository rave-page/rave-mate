package github

import (
	"testing"

	"rave.page/mate/internal/logbus"
)

// TestAuthFederation mirrors vrchat/federation_test.go for the GitHub link: an armed federation
// answers SignedIn/Login/Via ONLY without a local token, a local link always wins, Token() never
// answers while federated-only (the token never crosses the link), and ClearFederated reverts.
func TestAuthFederation(t *testing.T) {
	a := NewAuth(func() string { return "" }, logbus.New(16))

	// no local token + armed federation -> federated identity answers.
	a.SetFederated("peeruser", "desk")
	if !a.SignedIn() || !a.Federated() || a.Login() != "peeruser" || a.Via() != "desk" {
		t.Fatalf("federated drift: signedIn=%v fed=%v login=%q via=%q", a.SignedIn(), a.Federated(), a.Login(), a.Via())
	}
	if a.LocalSignedIn() {
		t.Fatal("LocalSignedIn must stay false while only federated")
	}
	// the token NEVER crosses: Token() errors while federated-only, so no local gist write.
	if _, err := a.Token(); err == nil {
		t.Fatal("Token() must error while federated-only")
	}

	// local link wins outright: federation masked, Login/Token = local, Via cleared.
	a.mu.Lock()
	a.tok = Token{Access: "tok_local", Login: "localuser"}
	a.mu.Unlock()
	if a.Federated() || a.Via() != "" {
		t.Fatalf("local link must win: fed=%v via=%q", a.Federated(), a.Via())
	}
	if a.Login() != "localuser" || !a.LocalSignedIn() {
		t.Fatalf("local identity must win: login=%q localSignedIn=%v", a.Login(), a.LocalSignedIn())
	}
	if tok, err := a.Token(); err != nil || tok != "tok_local" {
		t.Fatalf("local token must resolve: tok=%q err=%v", tok, err)
	}

	// local logout + explicit disarm -> signed out.
	a.mu.Lock()
	a.tok = Token{}
	a.mu.Unlock()
	a.ClearFederated()
	if a.SignedIn() || a.Federated() || a.Login() != "" {
		t.Fatalf("cleared federation still answering: signedIn=%v login=%q", a.SignedIn(), a.Login())
	}
}
