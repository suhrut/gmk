package cli

import "strconv"

func parseInt64Std(s string) (int64, error) { return strconv.ParseInt(s, 10, 64) }
func parseFloat64(s string) (float64, error) { return strconv.ParseFloat(s, 64) }
