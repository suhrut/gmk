package funcs

import "strconv"

// parseFloatStdlib wraps strconv.ParseFloat. Kept in its own file to
// localize the strconv import.
func parseFloatStdlib(s string) (float64, error) {
	return strconv.ParseFloat(s, 64)
}
