// Package procio holds helpers shared by the collectors: bounded reads,
// sanitizing of free text and safe percentages.
package procio

import (
	"fmt"
	"io"
	"io/fs"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxFileBytes bounds any single read: procfs reports size 0, so the size
// cannot be checked up front.
const MaxFileBytes = 1 << 20

// MaxTextRunes bounds free-text fields copied into tool output.
const MaxTextRunes = 128

// ReadFile reads name from fsys, failing if it is larger than limit bytes.
func ReadFile(fsys fs.FS, name string, limit int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("read %s: larger than %d bytes", name, limit)
	}
	return data, nil
}

// CleanText keeps only printable runes (dropping control and format
// characters such as ANSI escapes, bidi overrides and invalid UTF-8) and
// truncates to MaxTextRunes, so system-provided strings cannot smuggle
// terminal tricks or long payloads into tool output.
func CleanText(s string) string {
	var b strings.Builder
	n := 0
	for len(s) > 0 && n < MaxTextRunes {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		if r == utf8.RuneError && size <= 1 {
			continue
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			n++
		}
	}
	return b.String()
}

// Percent returns part/whole*100 rounded to two decimals, or 0 when whole
// is 0 (a NaN would make the result unencodable as JSON).
func Percent(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return math.Round(float64(part)/float64(whole)*10000) / 100
}
