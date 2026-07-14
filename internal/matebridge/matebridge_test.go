package matebridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBundleEnvelopeRoundTrip proves the common gist envelope carries a pointer module opaquely and
// the module decodes to its typed shape (the world's two-step parse: envelope gate, then module decode).
func TestBundleEnvelopeRoundTrip(t *testing.T) {
	ptr := PointerModule{
		Default:           "main",
		ByOperator:        []OperatorRef{{Operator: "DJ Nyx", Profile: "nyx-set", Priority: 10}},
		InstanceOwnerName: "DJ Nyx",
		ConfigURL:         "https://gist.githubusercontent.com/u/abc/raw/config.json",
		JoinInfo:          JoinInfo{DeepLink: "vrchat://launch?ref=x", Label: "Join the set"},
	}
	rawPtr, err := json.Marshal(ptr)
	if err != nil {
		t.Fatal(err)
	}
	env := Envelope{
		Schema: SchemaBundle, ContractVersion: ContractVersion, Seq: 42, UpdatedAt: "2026-07-14T00:00:00Z",
		Modules: map[string]json.RawMessage{ModulePointer: rawPtr},
	}
	blob, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}

	var got Envelope
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if got.Schema != SchemaBundle || got.Seq != 42 || got.ContractVersion != ContractVersion {
		t.Fatalf("envelope keys lost: %+v", got)
	}
	var gotPtr PointerModule
	if err := json.Unmarshal(got.Modules[ModulePointer], &gotPtr); err != nil {
		t.Fatal(err)
	}
	if gotPtr.InstanceOwnerName != "DJ Nyx" || len(gotPtr.ByOperator) != 1 || gotPtr.ByOperator[0].Priority != 10 {
		t.Fatalf("pointer module lost fields: %+v", gotPtr)
	}
}

// TestPresetPayloadOpaque proves the preset payload survives as the world's DTO verbatim.
func TestPresetPayloadOpaque(t *testing.T) {
	payload := json.RawMessage(`{"parallaxExaggeration":1.5,"minShellRadius":50}`)
	p := PresetEnvelope{
		Schema: PresetSchema, ContractVersion: ContractVersion, Kind: PresetBackdrop,
		ID: "skyline", Name: "Skyline", Source: "unity", Seq: 3, CoordSpace: "world",
		AssetRefs: []AssetRef{{Name: "Skyline", GUID: "deadbeef"}}, Payload: payload,
	}
	blob, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var got PresetEnvelope
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Payload) != string(payload) {
		t.Fatalf("payload mutated: %s", got.Payload)
	}
	if got.CoordSpace != "world" || got.AssetRefs[0].GUID != "deadbeef" {
		t.Fatalf("envelope fields lost: %+v", got)
	}
}

// TestAuthAndHealth proves the bearer gate and the health handshake over httptest.
func TestAuthAndHealth(t *testing.T) {
	s := New(Options{Version: "test-1", Token: "secret"})
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	// No token => 401 problem+json.
	resp, err := http.Get(srv.URL + PathPrefix + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, ProblemContentType) {
		t.Fatalf("want problem+json, got %q", ct)
	}
	_ = resp.Body.Close()

	// With token => 200 health, no capabilities (all gateways nil).
	req, _ := http.NewRequest(http.MethodGet, srv.URL+PathPrefix+"/health", nil)
	req.Header.Set(AuthHeader, "Bearer secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get(ContractHeader); got != "1" {
		t.Fatalf("contract header = %q", got)
	}
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	if !h.OK || h.RaveMateVersion != "test-1" || h.ContractVersion != ContractVersion {
		t.Fatalf("bad health: %+v", h)
	}
	if len(h.Capabilities) != 0 {
		t.Fatalf("want no caps with nil gateways, got %v", h.Capabilities)
	}
}

// TestMarshalSingleEnvelope proves a single-module gist carries the common keys + the module
// payload inlined at the top level under its kind key (the world's TryGetValue parse target).
func TestMarshalSingleEnvelope(t *testing.T) {
	ptr := PointerModule{Default: "main", InstanceOwnerName: "DJ Nyx"}
	inner, err := json.Marshal(ptr)
	if err != nil {
		t.Fatal(err)
	}
	blob, err := MarshalSingle(SchemaPointer, 7, "2026-07-15T00:00:00Z", ModulePointer, inner)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(blob, &top); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schema", "contractVersion", "seq", "updatedAt", ModulePointer} {
		if _, ok := top[k]; !ok {
			t.Fatalf("single envelope missing key %q: %s", k, blob)
		}
	}
	if _, ok := top["modules"]; ok {
		t.Fatal("single-module gist must NOT carry a modules map")
	}
	var gotPtr PointerModule
	if err := json.Unmarshal(top[ModulePointer], &gotPtr); err != nil {
		t.Fatal(err)
	}
	if gotPtr.InstanceOwnerName != "DJ Nyx" {
		t.Fatalf("pointer payload lost: %+v", gotPtr)
	}
	// A module key colliding with a reserved envelope key must be rejected (VRCJson hard-fails on dup keys).
	if _, err := MarshalSingle(SchemaPointer, 1, "", "seq", inner); err == nil {
		t.Fatal("expected reserved-key collision to error")
	}
}

// stubDirectory is a Directory that toggles Available() for the liveness test.
type stubDirectory struct{ up bool }

func (d *stubDirectory) Available() bool { return d.up }
func (d *stubDirectory) Friends(context.Context, int, int, bool) ([]Friend, error) {
	return []Friend{}, nil
}
func (d *stubDirectory) Groups(context.Context) ([]Group, error) { return []Group{}, nil }
func (d *stubDirectory) GroupMembers(context.Context, string, string, int, int) ([]GroupMember, bool, error) {
	return []GroupMember{}, false, nil
}
func (d *stubDirectory) Resolve(context.Context, []string) ([]Resolved, error) {
	return []Resolved{}, nil
}

// TestAvailablerGatesHealthAndRoutes proves a wired-but-not-ready gateway drops its capability from
// /health AND 501s its routes, then lights up live when Available() flips - the login/link gating.
func TestAvailablerGatesHealthAndRoutes(t *testing.T) {
	dir := &stubDirectory{up: false}
	s := New(Options{Token: "t", Directory: dir})

	call := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, PathPrefix+path, nil)
		req.Header.Set(AuthHeader, "Bearer t")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}

	// Not ready: friends 501, health omits vrchat.
	if w := call("/vrchat/friends"); w.Code != http.StatusNotImplemented {
		t.Fatalf("not-ready friends = %d, want 501", w.Code)
	}
	var h Health
	if err := json.Unmarshal(call("/health").Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.Capabilities {
		if c == CapVRChat {
			t.Fatal("vrchat advertised while gateway not Available")
		}
	}

	// Flip ready: friends 200, health advertises vrchat.
	dir.up = true
	if w := call("/vrchat/friends"); w.Code != http.StatusOK {
		t.Fatalf("ready friends = %d, want 200", w.Code)
	}
	h = Health{}
	if err := json.Unmarshal(call("/health").Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range h.Capabilities {
		if c == CapVRChat {
			found = true
		}
	}
	if !found {
		t.Fatalf("vrchat not advertised when Available: %v", h.Capabilities)
	}
}

// TestUnavailableGateway proves a nil gateway degrades to 501, never a panic.
func TestUnavailableGateway(t *testing.T) {
	s := New(Options{Token: "t"})
	req := httptest.NewRequest(http.MethodGet, PathPrefix+"/vrchat/friends", nil)
	req.Header.Set(AuthHeader, "Bearer t")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("want 501, got %d", w.Code)
	}
	var p Problem
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Type != ProblemNotImplemented || p.ContractVersion != ContractVersion {
		t.Fatalf("bad problem: %+v", p)
	}
}
