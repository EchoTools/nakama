package evr

import "testing"

func TestCombatSymbol(t *testing.T) {

	v := Symbol(0x3D5C3976578A3158)

	l := 5
	s := []byte("combat")
	t.Log(string(s) + " " + v.HexString())
	// cycle through a-z and set the last letter to the a-z value
	for i := 0; i < 256; i++ {
		s[l] = byte(96 + i)
		sym := ToSymbol(string(s))

		t.Log(string(s) + " " + sym.HexString())
	}

	t.Error()

}
