package sbc

import "testing"

func TestSameSymbolAliases(t *testing.T) {
	for _, alias := range []string{"000951", "sz000951", "000951.SZ", " 000951.sz "} {
		if !sameSymbol(alias, "000951.SZ") {
			t.Fatalf("alias rejected: %s", alias)
		}
	}
	for _, other := range []string{"000951.SH", "000001.SZ", "AAPL"} {
		if sameSymbol(other, "000951.SZ") {
			t.Fatalf("different security accepted: %s", other)
		}
	}
}
