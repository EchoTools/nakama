package evr

import "testing"

// The game folds each name byte into the hash sign-extended (MOVSX in
// echovr.exe CSymbol64::Lookup at 0x1400ce199; sxtb in libr15.so
// CalculateSymbolValue at 0x11cc760), so a byte >= 0x80 contributes
// 0xFFFFFFFFFFFFFFxx. Vectors are from EchoTools/nakama#710.
func TestToSymbol_SignExtendsHighBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want uint64
	}{
		{"ascii is unchanged", "main_menu", 0x1509ee27440e1d7b},
		{"utf-8 cafe", "caf\xc3\xa9", 0xcd0714df62754f5f},
		{"raw high bytes", "\xff\xfe", 0x1f17d45f0697bb92},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := CalculateSymbolValue(tt.in, 0xFFFFFFFFFFFFFFFF, hashLookupArray, 0); got != tt.want {
				t.Fatalf("CalculateSymbolValue(%q) = %#016x, want %#016x", tt.in, got, tt.want)
			}
			if got := uint64(ToSymbol(tt.in)); got != tt.want {
				t.Fatalf("ToSymbol(%q) = %#016x, want %#016x", tt.in, got, tt.want)
			}
		})
	}
}
