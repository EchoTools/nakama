package server

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const SpecialCharacters = "`~!@#$%^&*()-_=+[{]}\\|;:'\",.<>/?`"

type query struct {
	replacer *strings.Replacer
}

var Query query = func() query {
	replacements := make([]string, 0, len(SpecialCharacters)*2)
	for _, char := range SpecialCharacters {
		// Escape each character with a backslash
		replacements = append(replacements, string(char), "\\"+string(char))
	}
	replacer := strings.NewReplacer(replacements...)
	return query{
		replacer: replacer,
	}
}()

// blugeReservedChars are characters that Bluge's query string lexer treats as
// escape-able: a backslash before any of these strips the backslash and keeps
// the character literally. This means \+ becomes + in the token, which breaks
// regex patterns (+ is a quantifier). Regex metacharacters that are also in
// this set need double-backslash escaping so they survive the query parser.
const blugeReservedChars = "+-=&|><!(){}[]^\"~*?:\\/ "

// regexEscapeForBluge escapes a string for literal use inside a Bluge regex
// query (e.g. /pattern/). Bluge's lexer strips single backslashes from its
// reserved characters, so those need double-backslash escaping; all other
// regex metacharacters only need the standard single backslash.
func regexEscapeForBluge(s string) string {
	var result strings.Builder
	result.Grow(len(s) * 2)
	for _, ch := range s {
		q := regexp.QuoteMeta(string(ch))
		if q != string(ch) {
			// ch is a regex metacharacter escaped with a leading backslash.
			// If Bluge's lexer would strip that backslash, add an extra one.
			if strings.ContainsRune(blugeReservedChars, ch) {
				result.WriteByte('\\')
			}
		}
		result.WriteString(q)
	}
	return result.String()
}

func (query) CreateMatchPatternPartial(elems []string) string {
	if len(elems) == 0 {
		return ""
	}
	strs := make([]string, len(elems))
	for i, elem := range elems {
		strs[i] = regexEscapeForBluge(elem)
	}
	return fmt.Sprintf("/.*(%s).*/", strings.Join(strs, "|"))
}

// CreateMatchPattern returns a regex pattern that matches any of the provided elements.
func (query) CreateMatchPattern(elems []string) string {
	if len(elems) == 0 {
		return ""
	}
	strs := make([]string, len(elems))
	for i, elem := range elems {
		strs[i] = regexEscapeForBluge(elem)
	}
	return fmt.Sprintf("/(%s)/", strings.Join(strs, "|"))
}

// QuoteStringValue returns a quoted string representation of the input value.
func (q query) QuoteStringValue(input any) string {
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
	return q.replacer.Replace(s)
}
