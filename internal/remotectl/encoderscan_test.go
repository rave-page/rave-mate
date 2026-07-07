package remotectl

import "testing"

// fakeEncScan is an EncoderScanSource returning a fixed report.
type fakeEncScan struct{}

func (fakeEncScan) EncoderScan() string { return "encoder scan\nplan: encode nvenc" }

// TestEncoderScanRPC round-trips app.encoderscan: register on the server, fetch via the client.
func TestEncoderScanRPC(t *testing.T) {
	server, client := loopback()
	RegisterEncoderScan(server, fakeEncScan{})
	rc := NewClient(client, "server")
	text, err := rc.EncoderScan(ctx(t))
	if err != nil {
		t.Fatalf("encoderscan: %v", err)
	}
	if text != "encoder scan\nplan: encode nvenc" {
		t.Fatalf("unexpected text: %q", text)
	}
}

// Nil endpoint/source registration must be a no-op (mirrors the other Register* guards).
func TestRegisterEncoderScanNil(t *testing.T) {
	RegisterEncoderScan(nil, fakeEncScan{})
	server, client := loopback()
	RegisterEncoderScan(server, nil)
	if _, err := NewClient(client, "server").EncoderScan(ctx(t)); err == nil {
		t.Fatal("unregistered app.encoderscan must error")
	}
}
