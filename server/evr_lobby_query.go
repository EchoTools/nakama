package server

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/gofrs/uuid/v5"
)

var escapePattern = regexp.MustCompile(`[-+=&|><!(){}\[\]^"~*?:\\/ ]`)

var Query query

type query struct{}

func (query) Join(elems []string, sep string) string {
	strs := make([]string, len(elems))
	for i, elem := range elems {
		strs[i] = Query.Escape(elem)
	}
	return strings.Join(strs, sep)
}

func (query) JoinUUIDs(elems []uuid.UUID, sep string) string {
	strs := make([]string, len(elems))
	for i, elem := range elems {
		strs[i] = Query.Escape(elem)
	}
	return strings.Join(strs, sep)
}

func (query) Escape(input any) string {

	type stringer interface {
		String() string
	}

	s := ""
	switch v := input.(type) {
	case string:
		s = v
	case int:
		s = strconv.Itoa(v)
	case int64:
		s = strconv.FormatInt(v, 10)
	case uint:
		s = strconv.FormatUint(uint64(v), 10)
	case uint64:
		s = strconv.FormatUint(v, 10)
	case float32:
		s = strconv.FormatFloat(float64(v), 'f', -1, 32)
	case float64:
		s = strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v == true {
			s = "T"
		} else {
			s = "F"
		}
	case nil:
		s = "nil"
	case stringer:
		return v.String()
	default:
		panic("unsupported type")
	}

	return Query.escapeString(s)
}

func (query) escapeString(input string) string {
	return escapePattern.ReplaceAllString(input, `\$0`)
}
