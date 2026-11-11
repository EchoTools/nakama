package server

import "testing"

func TestNativeServerVersionCheck(t *testing.T) {
	admitted := []string{"2.0.0", "10.0.0", "v6.18.0-dev.9", "6.18.0-dev.9"}
	refused := []string{"1.9.9-dev.1", "2.0.0-dev.1"}
	for _, v := range admitted {
		if err := checkNativeServerVersion(v); err != nil {
			t.Errorf("%q should be admitted, got %v", v, err)
		}
	}
	for _, v := range refused {
		if err := checkNativeServerVersion(v); err == nil {
			t.Errorf("%q should be refused", v)
		}
	}
	for _, v := range []string{"", "banana", "2.0", "v"} {
		err := checkNativeServerVersion(v)
		if err == nil {
			t.Errorf("unparseable %q should be refused", v)
		}
	}
}
