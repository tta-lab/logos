package logos

import "testing"

func TestNewClient_EmptyAddr(t *testing.T) {
	c, err := NewClient("")
	if err != nil {
		t.Skipf("skipping: no default socket path available: %v", err)
	}
	if c == nil {
		t.Fatal("NewClient returned nil client")
	}
}
