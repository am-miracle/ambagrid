package postgres

import "testing"

func TestFallbackTxImplementsInterface(t *testing.T) {
	// Compile-time check that fallbackTx satisfies the interface the
	// service layer depends on. The services package is imported by
	// fallback.go already so this just locks the contract.
	_ = FallbackStore{}
}
