package evr

import (
	"encoding/binary"
)

func encode(buf []byte, v interface{}) {

	switch v := v.(type) {
	case uint64:
		b := make([]byte, 8)
		binary.LittleEndian.PutUint64(b, v)
	}
}
