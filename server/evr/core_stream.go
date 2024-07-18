package evr

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/davecgh/go-spew/spew"
	"github.com/go-playground/validator/v10"
	"github.com/gofrs/uuid/v5"
	"github.com/klauspost/compress/zstd"
)

type StreamMode int
type CompressionMode int

const (
	DecodeMode StreamMode = iota
	EncodeMode

	LittleEndian = iota
	BigEndian

	NoCompression = iota
	ZlibCompression
	ZstdCompression
)

var (
	errInvalidCompressionMode = errors.New("invalid compression mode")
	errInvalidMode            = errors.New("invalid mode")
	structValidate            = validator.New()
)

type EasyStream struct {
	r    *bytes.Reader
	w    *bytes.Buffer
	Mode StreamMode
}

func NewEasyStream(mode StreamMode, b []byte) *EasyStream {
	s := &EasyStream{
		Mode: mode,
	}
	switch mode {
	case DecodeMode:
		s.r = bytes.NewReader(b)
	case EncodeMode:
		s.w = bytes.NewBuffer(b)
	default:
	}
	return s
}

func (s *EasyStream) StreamSymbol(value *Symbol) error {
	return s.StreamNumber(binary.LittleEndian, value)
}

func (s *EasyStream) StreamNumber(order binary.ByteOrder, value any) error {
	switch s.Mode {
	case DecodeMode:
		return binary.Read(s.r, order, value)
	case EncodeMode:
		return binary.Write(s.w, order, value)
	default:
		return errInvalidMode
	}
}

func (s *EasyStream) StreamIpAddress(data *net.IP) error {
	b := make([]byte, net.IPv4len)
	copy(b, (*data).To4())
	if err := s.StreamBytes(&b, net.IPv4len); err != nil {
		return err
	}
	*data = net.IP(b)
	return nil
}

func (s *EasyStream) StreamByte(value *byte) error {
	var err error
	switch s.Mode {
	case DecodeMode:
		*value, err = s.r.ReadByte()
		return err
	case EncodeMode:
		return s.w.WriteByte(*value)
	default:
		return errInvalidMode
	}
}

// StreamBytes reads or writes bytes to the stream based on the mode of the EasyStream.
// If the mode is ReadMode, it reads bytes from the stream and stores them in the provided data slice.
// If the length parameter is -1, it reads all available bytes from the stream.
// If the mode is WriteMode, it writes the bytes from the provided data slice to the stream.
// It returns an error if there is any issue with reading or writing the bytes.
func (s *EasyStream) StreamBytes(dst *[]byte, l int) error {
	var err error
	switch s.Mode {
	case DecodeMode:
		if l == 0 {
			return nil
		}
		if l == -1 {
			l = s.r.Len()
		}
		*dst = make([]byte, l)
		_, err = s.r.Read(*dst)
	case EncodeMode:
		_, err = s.w.Write(*dst)
	default:
		err = errInvalidMode
	}
	return err
}

func (s *EasyStream) StreamString(value *string, length int) error {
	var err error

	b := make([]byte, length)
	switch s.Mode {
	case DecodeMode:
		_, err = s.r.Read(b)
		*value = string(bytes.TrimRight(b, "\x00"))
	case EncodeMode:
		copy(b, []byte(*value))
		// Zero pad the value up to the length
		for i := len(*value); i < length; i++ {
			b[i] = 0
		}
		err = s.StreamBytes(&b, len(b))
	default:
		err = errInvalidMode
	}
	return err
}

func (s *EasyStream) StreamNullTerminatedString(str *string) error {
	b := make([]byte, 0, len(*str))
	switch s.Mode {
	case DecodeMode:
		for {
			if c, err := s.r.ReadByte(); err != nil {
				return err
			} else if c == 0x0 {
				break
			} else {
				b = append(b, c)
			}
		}
		*str = string(b)
		return nil
	case EncodeMode:
		bytes := []byte(*str)
		bytes = append(bytes, 0)
		return s.StreamBytes(&bytes, len(bytes))
	default:
		return errors.New("StreamNullTerminatedString: invalid mode")
	}
}

func (s *EasyStream) StreamStruct(obj Serializable) error {
	return obj.Stream(s)
}

// Stream multiple GUIDs
func (s *EasyStream) StreamGuids(uuids *[]uuid.UUID) error {
	var err error
	for i := 0; i < len(*uuids); i++ {
		if err = s.StreamGuid(&(*uuids)[i]); err != nil {
			return err
		}
	}
	return nil
}

// Microsoft's GUID has some bytes re-ordered.
func (s *EasyStream) StreamGuid(uuid *uuid.UUID) error {
	var err error
	var b []byte
	fn := func(p *[]byte) {
		b := *p
		// flip the first four bytes, and the next two pairs
		b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
		b[4], b[5] = b[5], b[4]
		b[6], b[7] = b[7], b[6]
	}
	switch s.Mode {
	case DecodeMode:
		b = make([]byte, 16)
		if err = s.StreamBytes(&b, 16); err != nil {
			return err
		}
		fn(&b)
		if err = uuid.UnmarshalBinary(b); err != nil {
			return err
		}
	case EncodeMode:
		if b, err = uuid.MarshalBinary(); err != nil {
			return err
		}
		fn(&b)
		return s.StreamBytes(&b, 16)
	default:
		return errInvalidMode
	}
	return nil
}

func (s *EasyStream) StreamStringTable(entries *[]string) error {
	var err error
	var strings []string
	logCount := uint64(len(*entries))
	if err = s.StreamNumber(binary.LittleEndian, &logCount); err != nil {
		return err
	}
	switch s.Mode {
	case DecodeMode:
		strings = make([]string, logCount)
		offsets := make([]uint32, logCount)
		offsets[0] = 0
		for i := 1; i < int(logCount); i++ {
			off := uint32(0)
			if err = s.StreamNumber(binary.LittleEndian, &off); err != nil {
				return err
			}
			offsets[i] = off
		}

		bufferStart := s.Position()
		for i, off := range offsets {
			if err = s.SetPosition(bufferStart + int(off)); err != nil {
				return err
			}
			if err = s.StreamNullTerminatedString(&strings[i]); err != nil {
				return err
			}
		}
		*entries = strings
	case EncodeMode:
		// write teh offfsets
		for i := 0; i < int(logCount-1); i++ {
			off := uint32(len((*entries)[i]) + 1)
			if err = s.StreamNumber(binary.LittleEndian, &off); err != nil {
				return err
			}
		}
		// write the strings
		for _, str := range *entries {
			if s.StreamNullTerminatedString(&str); err != nil {
				return err
			}
		}
	default:
		return errInvalidMode
	}
	return nil
}

func (s *EasyStream) Bytes() []byte {
	if s.Mode == DecodeMode {
		b := make([]byte, s.r.Len())
		s.r.Read(b)
		return b
	}
	return s.w.Bytes()
}

func (s *EasyStream) Len() int {
	if s.Mode == DecodeMode {
		return s.r.Len()
	}
	return s.w.Len()
}

func (s *EasyStream) Position() int {
	if s.Mode == DecodeMode {
		pos, _ := s.r.Seek(0, 1) // Current position
		return int(pos)
	}
	return s.w.Len()
}

func (s *EasyStream) SetPosition(pos int) error {
	if s.Mode == DecodeMode {
		if _, err := s.r.Seek(int64(pos), 0); err != nil {
			return errors.New("SetPosition failed: " + err.Error())
		}
		return nil
	}
	if pos > s.w.Len() {
		return errors.New("SetPosition: position out of range")
	}
	s.w.Truncate(pos)
	return nil
}

func (s *EasyStream) Reset() {
	if s.Mode == DecodeMode {
		s.r.Reset(s.Bytes())
	} else {
		s.w.Reset()
	}
}

type Serializable interface {
	Stream(s *EasyStream) error
}

func RunErrorFunctions(funcs []func() error) error {
	var err error
	var fn func() error
	for _, fn = range funcs {
		if err = fn(); err != nil {
			return err
		}
	}
	return nil
}

func StringifyStruct(s interface{}) string {
	return spew.Sdump(s)
}

func ValidateStruct(s interface{}) error {

	err := structValidate.Struct(s)
	if err != nil {
		if _, ok := err.(*validator.InvalidValidationError); ok {
			return err
		}
		return err.(validator.ValidationErrors)
	}
	return nil
}

// Reads the bytes to the end of the stream or the nullTerminator
func ReadBytes(r io.ByteReader, dst *bytes.Buffer, isNullTerminated bool) error {
	var err error
	var b byte
	for err != io.EOF {
		if b, err = r.ReadByte(); isNullTerminated && b == '\x00' {
			break
		} else if err != nil {
			return err
		}
		dst.WriteByte(b)
	}
	return nil
}

func (s *EasyStream) StreamJson(data interface{}, isNullTerminated bool, compressionMode CompressionMode) error {
	var buf bytes.Buffer
	var err error
	switch s.Mode {
	case DecodeMode:
		switch compressionMode {
		case NoCompression:
			if err := ReadBytes(s.r, &buf, isNullTerminated); err != nil && err != io.EOF {
				return fmt.Errorf("read bytes error: %w", err)
			}
		case ZlibCompression:
			l64 := uint64(0)
			if err = binary.Read(s.r, binary.LittleEndian, &l64); err != nil {
				return fmt.Errorf("zlib length read error: %w", err)
			}
			r, err := zlib.NewReader(s.r)
			if err != nil {
				return err
			}
			io.Copy(&buf, r)
			r.Close()
		case ZstdCompression:
			l32 := uint32(0)
			if err = binary.Read(s.r, binary.LittleEndian, &l32); err != nil {
				return fmt.Errorf("zstd length read error: %w", err)
			}
			r, err := zstd.NewReader(s.r)
			if err != nil {
				return err
			}
			io.Copy(&buf, r)
			r.Close()
		default:
			return errInvalidCompressionMode
		}
		if isNullTerminated && buf.Len() > 0 && buf.Bytes()[buf.Len()-1] == 0x0 {
			buf.Truncate(buf.Len() - 1)
		}

		if err = json.Unmarshal(buf.Bytes(), &data); err != nil {
			return fmt.Errorf("unmarshal json error: %w: %s", err, buf.Bytes())
		}
		return nil
	case EncodeMode:
		b, err := json.Marshal(data)
		if err != nil {
			return fmt.Errorf("marshal json error: %w", err)
		}
		if isNullTerminated {
			b = append(b, 0x0)
		}
		switch compressionMode {
		case ZlibCompression:
			if err := binary.Write(s.w, binary.LittleEndian, uint64(len(b))); err != nil {
				return fmt.Errorf("write error: %w", err)
			}
			w := zlib.NewWriter(s.w)
			if _, err = w.Write(b); err != nil {
				return err
			}
			w.Close()
		case ZstdCompression:
			if err := binary.Write(s.w, binary.LittleEndian, uint32(len(b))); err != nil {
				return fmt.Errorf("write error: %w", err)
			}
			w, err := zstd.NewWriter(s.w)
			if err != nil {
				return err
			}
			if _, err = w.Write(b); err != nil {
				return err
			}
			w.Close()
		case NoCompression:
			if _, err := s.w.Write(b); err != nil {
				return err
			}
		default:
			return errInvalidCompressionMode
		}

	default:
		return errors.New("StreamJson: invalid mode")
	}
	return nil
}

func (s *EasyStream) StreamCompressedBytes(data []byte, isNullTerminated bool, compressionMode CompressionMode) error {
	buf := bytes.Buffer{}
	var err error
	switch s.Mode {
	case DecodeMode:
		switch compressionMode {
		case NoCompression:
			if err := ReadBytes(s.r, &buf, isNullTerminated); err != nil && err != io.EOF {
				return fmt.Errorf("read bytes error: %w", err)
			}
		case ZlibCompression:
			l64 := uint64(0)
			if err = binary.Read(s.r, binary.LittleEndian, &l64); err != nil {
				return fmt.Errorf("zlib length read error: %w", err)
			}
			r, err := zlib.NewReader(s.r)
			if err != nil {
				return err
			}
			_, err = io.Copy(&buf, r)
			if err != nil {
				return err
			}
			r.Close()
		case ZstdCompression:
			l32 := uint32(0)
			if err = binary.Read(s.r, binary.LittleEndian, &l32); err != nil {
				return fmt.Errorf("zstd length read error: %w", err)
			}
			r, err := zstd.NewReader(s.r)
			if err != nil {
				return err
			}
			_, err = io.Copy(&buf, r)
			if err != nil {
				return err
			}
			r.Close()
		default:
			return errInvalidCompressionMode
		}
		b := buf.Bytes()
		if isNullTerminated && len(b) > 0 && b[len(b)-1] == 0x0 {
			b = b[:len(b)-1]
		}
		data = b
		return nil
	case EncodeMode:
		b := data
		if isNullTerminated {
			b = append(b, 0x0)
		}
		switch compressionMode {
		case ZlibCompression:
			l64 := uint64(len(b))
			if err := binary.Write(s.w, binary.LittleEndian, l64); err != nil {
				return fmt.Errorf("write error: %w", err)
			}
			w := zlib.NewWriter(s.w)
			if _, err = w.Write(b); err != nil {
				return err
			}
			w.Close()
		case ZstdCompression:
			l32 := uint32(len(b))
			if err := binary.Write(s.w, binary.LittleEndian, l32); err != nil {
				return fmt.Errorf("write error: %w", err)
			}
			w, err := zstd.NewWriter(s.w, zstd.WithEncoderLevel(1))
			if err != nil {
				return err
			}
			if _, err = w.Write(b); err != nil {
				return err
			}
			w.Close()
		case NoCompression:
			if _, err := s.w.Write(b); err != nil {
				return err
			}
		default:
			return errInvalidCompressionMode
		}
	default:
		return errors.New("StreamJson: invalid mode")
	}
	return nil
}

func GetRandomBytes(l int) []byte {
	b := make([]byte, l)
	_, err := rand.Read(b)
	if err != nil {
		log.Fatalf("error generating random bytes: %v", err)
	}
	return b
}

func (s *EasyStream) Skip(count int) (err error) {
	if s.Mode == DecodeMode {
		_, err = s.r.Seek(int64(count), io.SeekCurrent)
	} else {
		_, err = s.w.Write(make([]byte, count))
	}
	return
}
