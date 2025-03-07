package evr

import "strings"

func HashString(s string, t [0x100]uint64) (h uint64) {
	s = strings.ToLower(s)
	return HashBytes([]byte(s), t)
}

func HashBytes(b []byte, t [0x100]uint64) (h uint64) {
	h = 0xffffffffffffffff
	for _, v := range b {
		h = uint64(v) ^ t[h>>56] ^ (h << 8)
	}
	return h
}
