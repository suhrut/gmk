package expr

import (
	"strconv"
	"strings"
)

// Helpers that wrap strconv. Centralised so the parser file doesn't
// pull strconv directly (keeps imports in parser.go minimal) and so
// the parser can share the helpers with eval.

func strconvParseInt(s string) (int64, error) {
	return strconv.ParseInt(s, 10, 64)
}

func strconvParseFloat(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}

// trimSpace alias for parser readability; standalone so we don't import
// strings in parser.go for one call.
var trimSpace = strings.TrimSpace
