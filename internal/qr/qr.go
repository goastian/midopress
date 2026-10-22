// Package qr renders QR codes as SVG or PNG. It is a thin layer over a pure Go
// encoder so the rest of the code does not depend on that library.
package qr

import (
	"errors"
	"fmt"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

const quietZone = 4

// Level is the error correction level.
type Level int

// Error correction levels, from the least to the most redundancy.
const (
	LevelL Level = iota
	LevelM
	LevelQ
	LevelH
)

type config struct {
	level Level
	dark  string
	light string
}

// Option customises SVG.
type Option func(*config)

// WithLevel sets the error correction level (default LevelM).
func WithLevel(l Level) Option { return func(c *config) { c.level = l } }

// WithColors sets the dark and light colors of the SVG. An empty light color
// leaves the background transparent. The default dark color is currentColor.
func WithColors(dark, light string) Option {
	return func(c *config) { c.dark, c.light = dark, light }
}

func (l Level) lib() qrcode.RecoveryLevel {
	switch l {
	case LevelL:
		return qrcode.Low
	case LevelQ:
		return qrcode.High
	case LevelH:
		return qrcode.Highest
	default:
		return qrcode.Medium
	}
}

func encode(data string, level Level) (*qrcode.QRCode, error) {
	if data == "" {
		return nil, errors.New("qr: empty data")
	}
	q, err := qrcode.New(data, level.lib())
	if err != nil {
		return nil, fmt.Errorf("qr: %w", err)
	}
	return q, nil
}

// Matrix returns the module matrix of data without quiet zone, true being dark.
func Matrix(data string, level Level) ([][]bool, error) {
	q, err := encode(data, level)
	if err != nil {
		return nil, err
	}
	q.DisableBorder = true
	return q.Bitmap(), nil
}

// SVG renders data as a self-contained <svg> element. The viewBox is in
// modules and includes a quiet zone of four modules.
func SVG(data string, opts ...Option) (string, error) {
	cfg := config{level: LevelM, dark: "currentColor"}
	for _, o := range opts {
		o(&cfg)
	}
	m, err := Matrix(data, cfg.level)
	if err != nil {
		return "", err
	}
	n := len(m)
	total := n + 2*quietZone

	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d" shape-rendering="crispEdges" role="img" aria-label="QR code">`, total, total)
	if cfg.light != "" {
		fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="%s"/>`, total, total, cfg.light)
	}
	fmt.Fprintf(&b, `<path fill="%s" d="`, cfg.dark)
	for y, row := range m {
		for x := 0; x < n; {
			if !row[x] {
				x++
				continue
			}
			start := x
			for x < n && row[x] {
				x++
			}
			fmt.Fprintf(&b, "M%d %dh%dv1h-%dz", start+quietZone, y+quietZone, x-start, x-start)
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String(), nil
}

// PNG renders data as a PNG of at least size pixels per side, level M.
func PNG(data string, size int) ([]byte, error) {
	q, err := encode(data, LevelM)
	if err != nil {
		return nil, err
	}
	return q.PNG(size)
}
