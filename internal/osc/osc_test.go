package osc

import (
	"bytes"
	"math"
	"testing"
)

func TestEncodeFloat(t *testing.T) {
	got, err := encode("/a/b", float32(1.0))
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		'/', 'a', '/', 'b', 0, 0, 0, 0, // address padded
		',', 'f', 0, 0, // type tags padded
		0x3f, 0x80, 0x00, 0x00, // float32 1.0 big-endian
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encode float\n got %v\nwant %v", got, want)
	}
}

func TestEncodeString(t *testing.T) {
	got, err := encode("/s", "hi")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		'/', 's', 0, 0, // address padded
		',', 's', 0, 0, // type tags padded
		'h', 'i', 0, 0, // string padded
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encode string\n got %v\nwant %v", got, want)
	}
}

func TestEncodeBool(t *testing.T) {
	got, err := encode("/b", true, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		'/', 'b', 0, 0, // address padded
		',', 'T', 'F', 0, // type tags padded, no payload for bools
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encode bool\n got %v\nwant %v", got, want)
	}
}

func TestEncodeUnsupported(t *testing.T) {
	if _, err := encode("/x", 3.14); err == nil {
		t.Fatal("expected error for unsupported arg type")
	}
}

func TestQuatToEulerIdentity(t *testing.T) {
	x, y, z := QuatToEulerZXY(0, 0, 0, 1)
	if !near(x, 0) || !near(y, 0) || !near(z, 0) {
		t.Fatalf("identity quat -> (%v,%v,%v), want ~0", x, y, z)
	}
}

func TestQuatToEuler90Y(t *testing.T) {
	s := float32(math.Sqrt2 / 2) // sin/cos of 45deg
	x, y, z := QuatToEulerZXY(0, s, 0, s)
	if !near(x, 0) || !near(y, 90) || !near(z, 0) {
		t.Fatalf("90deg-about-Y quat -> (%v,%v,%v), want ~(0,90,0)", x, y, z)
	}
}

func near(got, want float32) bool {
	return math.Abs(float64(got-want)) < 1e-3
}
