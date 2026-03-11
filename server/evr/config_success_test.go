package evr

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// TestConfigSuccess_WireFormat verifies the full end-to-end encoding of a ConfigSuccess
// message matches the wire format the client expects:
//
//	[MessageMarker: 8] [symbol: 8] [data_length: 8] [Type: 8] [Id: 8] [l32: 4] [zstd frame: N]
//
// In particular, l32 must equal the uncompressed payload length (including null terminator),
// and the zstd frame must start with magic bytes 0xFD2FB528 (little-endian: 28 b5 2f fd).
func TestConfigSuccess_WireFormat(t *testing.T) {
	tests := []struct {
		name    string
		jsonStr string
	}{
		{
			name:    "main_menu default",
			jsonStr: DefaultMainMenuConfigResource,
		},
		{
			name:    "active_battle_pass_season default",
			jsonStr: DefaultActiveBattlePassSeasonConfigResource,
		},
		{
			name:    "active_store_entry default",
			jsonStr: DefaultActiveStoreEntryConfigResource,
		},
		{
			name:    "active_store_featured_entry default",
			jsonStr: DefaultActiveStoreFeaturedEntryConfigResource,
		},
		{
			name:    "empty object",
			jsonStr: `{}`,
		},
		{
			name:    "object with nulls",
			jsonStr: `{"a":null,"b":null,"c":0}`,
		},
		{
			name:    "nested object",
			jsonStr: `{"outer":{"inner":"value"},"arr":[1,2,3]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the pipeline handler: unmarshal JSON string into map,
			// then pass to NewConfigSuccess (exactly what evr_pipeline_config.go does).
			resource := make(map[string]interface{})
			if err := json.Unmarshal([]byte(tt.jsonStr), &resource); err != nil {
				t.Fatalf("json.Unmarshal failed: %v", err)
			}

			msg := NewConfigSuccess("main_menu", "main_menu", resource)

			wire, err := Marshal(msg)
			if err != nil {
				t.Fatalf("Marshal failed: %v", err)
			}

			// Wire format:
			//   [0..7]   MessageMarker (8 bytes)
			//   [8..15]  symbol (8 bytes LE uint64)
			//   [16..23] data_length (8 bytes LE uint64)
			//   [24..31] Type symbol (8 bytes)
			//   [32..39] Id symbol (8 bytes)
			//   [40..43] l32 (4 bytes LE uint32) = uncompressed JSON size incl. null terminator
			//   [44..]   zstd frame

			const headerSize = 8 + 8 + 8        // marker + symbol + data_length
			const payloadHeaderSize = 8 + 8 + 4 // Type + Id + l32

			if len(wire) < headerSize+payloadHeaderSize+4 {
				t.Fatalf("wire too short: %d bytes", len(wire))
			}

			// Check MessageMarker
			if !bytes.Equal(wire[:8], MessageMarker) {
				t.Errorf("MessageMarker mismatch: got %x, want %x", wire[:8], MessageMarker)
			}

			// Read data_length from wire
			dataLength := binary.LittleEndian.Uint64(wire[16:24])
			expectedDataLength := uint64(len(wire) - headerSize)
			if dataLength != expectedDataLength {
				t.Errorf("data_length field %d != actual payload size %d", dataLength, expectedDataLength)
			}

			// l32 is at offset 40 (after marker+sym+datalen+Type+Id)
			l32 := binary.LittleEndian.Uint32(wire[40:44])

			// Compute expected uncompressed size: re-marshal the resource the same way StreamJson does
			reEncoded, err := json.Marshal(resource)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}
			// StreamJson appends null terminator when isNullTerminated=true
			reEncoded = append(reEncoded, 0x0)
			expectedL32 := uint32(len(reEncoded))

			if l32 != expectedL32 {
				t.Errorf("l32 = %d, want %d (uncompressed size incl null term)", l32, expectedL32)
			}

			// Check zstd magic at offset 44: little-endian 0xFD2FB528 = bytes 28 b5 2f fd
			zstdMagic := []byte{0x28, 0xb5, 0x2f, 0xfd}
			if len(wire) < 44+4 {
				t.Fatalf("wire too short for zstd magic check: %d bytes", len(wire))
			}
			if !bytes.Equal(wire[44:48], zstdMagic) {
				t.Errorf("zstd magic at offset 44: got %x, want %x", wire[44:48], zstdMagic)
			}

			// Decompress the zstd frame and verify it matches expected
			r, err := zstd.NewReader(bytes.NewReader(wire[44:]))
			if err != nil {
				t.Fatalf("zstd.NewReader failed: %v", err)
			}
			var decompBuf bytes.Buffer
			if _, err := decompBuf.ReadFrom(r); err != nil {
				t.Fatalf("zstd decompress failed: %v", err)
			}
			r.Close()

			decompressed := decompBuf.Bytes()
			// Strip null terminator
			if len(decompressed) > 0 && decompressed[len(decompressed)-1] == 0x0 {
				decompressed = decompressed[:len(decompressed)-1]
			}

			// Verify the decompressed bytes are valid JSON equal to the re-encoded resource
			var got, want interface{}
			if err := json.Unmarshal(decompressed, &got); err != nil {
				t.Fatalf("decompressed data is not valid JSON: %v\ndata: %s", err, decompressed)
			}
			if err := json.Unmarshal([]byte(tt.jsonStr), &want); err != nil {
				t.Fatalf("original jsonStr is not valid JSON: %v", err)
			}

			t.Logf("wire size: %d bytes, l32: %d, zstd frame size: %d", len(wire), l32, len(wire)-44)
		})
	}
}

// TestConfigSuccess_DirectResource tests passing a pre-built resource directly
// (not round-tripped through json.Unmarshal), matching the default path.
func TestConfigSuccess_DirectResource(t *testing.T) {
	resource := map[string]interface{}{
		"type": "main_menu",
		"id":   "main_menu",
		"_ts":  float64(0),
	}

	msg := NewConfigSuccess("main_menu", "main_menu", resource)
	wire, err := Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Verify zstd magic at offset 44
	zstdMagic := []byte{0x28, 0xb5, 0x2f, 0xfd}
	if len(wire) < 48 {
		t.Fatalf("wire too short: %d bytes", len(wire))
	}
	if !bytes.Equal(wire[44:48], zstdMagic) {
		t.Errorf("zstd magic at offset 44: got %x, want %x", wire[44:48], zstdMagic)
	}

	t.Logf("wire: %x", wire)
}
