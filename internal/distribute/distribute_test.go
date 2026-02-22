package distribute

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestDistance(t *testing.T) {
	a, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000001")
	b, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000002")
	expected, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000003")

	dist := Distance(a, b)
	if !bytes.Equal(dist, expected) {
		t.Errorf("expected %x, got %x", expected, dist)
	}
}

func TestCmpDistance(t *testing.T) {
	d1, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000001")
	d2, _ := hex.DecodeString("0000000000000000000000000000000000000000000000000000000000000002")

	if CmpDistance(d1, d2) != -1 {
		t.Errorf("expected d1 < d2")
	}
	if CmpDistance(d2, d1) != 1 {
		t.Errorf("expected d2 > d1")
	}
	if CmpDistance(d1, d1) != 0 {
		t.Errorf("expected d1 == d1")
	}
}
