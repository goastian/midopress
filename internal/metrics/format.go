package metrics

import (
	"math"
	"strconv"
)

func appendFloat(b []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(b, "NaN"...)
	case math.IsInf(f, 1):
		return append(b, "+Inf"...)
	case math.IsInf(f, -1):
		return append(b, "-Inf"...)
	}
	return strconv.AppendFloat(b, f, 'g', -1, 64)
}

func appendHelp(b []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b = append(b, '\\', '\\')
		case '\n':
			b = append(b, '\\', 'n')
		default:
			b = append(b, s[i])
		}
	}
	return b
}

func appendLabelValue(b []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			b = append(b, '\\', '\\')
		case '"':
			b = append(b, '\\', '"')
		case '\n':
			b = append(b, '\\', 'n')
		default:
			b = append(b, s[i])
		}
	}
	return b
}

func appendHeader(b []byte, d *desc) []byte {
	b = append(b, "# HELP "...)
	b = append(b, d.name...)
	b = append(b, ' ')
	b = appendHelp(b, d.help)
	b = append(b, "\n# TYPE "...)
	b = append(b, d.name...)
	b = append(b, ' ')
	b = append(b, d.typ...)
	return append(b, '\n')
}

// appendSeries writes `name{labels[,extra]} ` ready for a value.
func appendSeries(b []byte, name string, labels, values []string, extraName, extraValue string) []byte {
	b = append(b, name...)
	if len(labels) > 0 || extraName != "" {
		b = append(b, '{')
		for i, l := range labels {
			if i > 0 {
				b = append(b, ',')
			}
			b = append(b, l...)
			b = append(b, '=', '"')
			b = appendLabelValue(b, values[i])
			b = append(b, '"')
		}
		if extraName != "" {
			if len(labels) > 0 {
				b = append(b, ',')
			}
			b = append(b, extraName...)
			b = append(b, '=', '"')
			b = appendLabelValue(b, extraValue)
			b = append(b, '"')
		}
		b = append(b, '}')
	}
	return append(b, ' ')
}

func appendUintSample(b []byte, name string, labels, values []string, v uint64) []byte {
	b = appendSeries(b, name, labels, values, "", "")
	b = strconv.AppendUint(b, v, 10)
	return append(b, '\n')
}

func appendFloatSample(b []byte, name string, labels, values []string, v float64) []byte {
	b = appendSeries(b, name, labels, values, "", "")
	b = appendFloat(b, v)
	return append(b, '\n')
}
