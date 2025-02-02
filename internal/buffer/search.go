package buffer

import (
	"regexp"
	"unicode/utf8"

	"github.com/zyedidia/micro/v2/internal/util"
)

// RegexpGroup combines a Regexp with padded versions.
// We want "^" and "$" to match only the beginning/end of a line, not that
// of the search region somewhere in the middle of a line. In that case we
// use padded regexps to require a rune before or after the match. (This
// also affects other empty-string patters like "\\b".)
type RegexpGroup [4]*regexp.Regexp

const (
	padStart = 1 << iota
	padEnd
)

// NewRegexpGroup creates a RegexpGroup from a string
func NewRegexpGroup(s string) (RegexpGroup, error) {
	var r RegexpGroup
	var err error
	r[0], err = regexp.Compile(s)
	if err == nil {
		r[padStart] = regexp.MustCompile(".(?:" + s + ")")
		r[padEnd] = regexp.MustCompile("(?:" + s + ").")
		r[padStart|padEnd] = regexp.MustCompile(".(?:" + s + ").")
	}
	return r, err
}

func findLineParams(b *Buffer, start, end Loc, i int) ([]byte, int, int) {
	l := b.LineBytes(i)
	charpos := 0
	padMode := 0

	if i == end.Y {
		nchars := util.CharacterCount(l)
		end.X = util.Clamp(end.X, 0, nchars)
		if end.X < nchars {
			l = util.SliceStart(l, end.X+1)
			padMode |= padEnd
		}
	}

	if i == start.Y {
		nchars := util.CharacterCount(l)
		start.X = util.Clamp(start.X, 0, nchars)
		if start.X > 0 {
			charpos = start.X - 1
			l = util.SliceEnd(l, charpos)
			padMode |= padStart
		}
	}

	return l, charpos, padMode
}

func (b *Buffer) findDown(r RegexpGroup, start, end Loc) ([2]Loc, bool) {
	lastcn := util.CharacterCount(b.LineBytes(b.LinesNum() - 1))
	if start.Y > b.LinesNum()-1 {
		start.X = lastcn - 1
	}
	if end.Y > b.LinesNum()-1 {
		end.X = lastcn
	}
	start.Y = util.Clamp(start.Y, 0, b.LinesNum()-1)
	end.Y = util.Clamp(end.Y, 0, b.LinesNum()-1)

	if start.GreaterThan(end) {
		start, end = end, start
	}

	for i := start.Y; i <= end.Y; i++ {
		l, charpos, padMode := findLineParams(b, start, end, i)

		match := r[padMode].FindIndex(l)

		if match != nil {
			if padMode&padStart != 0 {
				_, size := utf8.DecodeRune(l[match[0]:])
				match[0] += size
			}
			if padMode&padEnd != 0 {
				_, size := utf8.DecodeLastRune(l[:match[1]])
				match[1] -= size
			}
			start := Loc{charpos + util.RunePos(l, match[0]), i}
			end := Loc{charpos + util.RunePos(l, match[1]), i}
			return [2]Loc{start, end}, true
		}
	}
	return [2]Loc{}, false
}

func (b *Buffer) findUp(r RegexpGroup, start, end Loc) ([2]Loc, bool) {
	lastcn := util.CharacterCount(b.LineBytes(b.LinesNum() - 1))
	if start.Y > b.LinesNum()-1 {
		start.X = lastcn - 1
	}
	if end.Y > b.LinesNum()-1 {
		end.X = lastcn
	}
	start.Y = util.Clamp(start.Y, 0, b.LinesNum()-1)
	end.Y = util.Clamp(end.Y, 0, b.LinesNum()-1)

	if start.GreaterThan(end) {
		start, end = end, start
	}

	var match [2]Loc
	for i := end.Y; i >= start.Y; i-- {
		charCount := util.CharacterCount(b.LineBytes(i))
		from := Loc{0, i}.Clamp(start, end)
		to := Loc{charCount, i}.Clamp(start, end)

		n := b.findAllFunc(r, from, to, func(from, to Loc) {
			match = [2]Loc{from, to}
		})

		if n != 0 {
			return match, true
		}
	}
	return match, false
}

func (b *Buffer) findAllFunc(r RegexpGroup, start, end Loc, f func(from, to Loc)) int {
	loc := start
	nfound := 0
	for {
		match, found := b.findDown(r, loc, end)
		if !found {
			break
		}
		nfound++
		f(match[0], match[1])
		if match[0] != match[1] {
			loc = match[1]
		} else if match[1] != end {
			loc = match[1].Move(1, b)
		} else {
			break
		}
	}
	return nfound
}

// FindNext finds the next occurrence of a given string in the buffer
// It returns the start and end location of the match (if found) and
// a boolean indicating if it was found
// May also return an error if the search regex is invalid
func (b *Buffer) FindNext(s string, start, end, from Loc, down bool, useRegex bool) ([2]Loc, bool, error) {
	if s == "" {
		return [2]Loc{}, false, nil
	}

	if !useRegex {
		s = regexp.QuoteMeta(s)
	}

	if b.Settings["ignorecase"].(bool) {
		s = "(?i)" + s
	}

	r, err := NewRegexpGroup(s)
	if err != nil {
		return [2]Loc{}, false, err
	}

	var found bool
	var l [2]Loc
	if down {
		l, found = b.findDown(r, from, end)
		if !found {
			l, found = b.findDown(r, start, end)
		}
	} else {
		l, found = b.findUp(r, from, start)
		if !found {
			l, found = b.findUp(r, end, start)
		}
	}
	return l, found, nil
}

// ReplaceRegex replaces all occurrences of 'search' with 'replace' in the given area
// and returns the number of replacements made and the number of characters
// added or removed on the last line of the range
func (b *Buffer) ReplaceRegex(start, end Loc, search RegexpGroup, replace []byte, captureGroups bool) (int, int) {
	if start.GreaterThan(end) {
		start, end = end, start
	}

	charsEnd := util.CharacterCount(b.LineBytes(end.Y))

	var deltas []Delta
	nfound := b.findAllFunc(search, start, end, func(from, to Loc) {
		var newText []byte
		if captureGroups {
			newText = search[0].ReplaceAll(b.Substr(from, to), replace)
		} else {
			newText = replace
		}
		deltas = append(deltas, Delta{newText, from, to})
	})
	b.MultipleReplace(deltas)

	deltaX := util.CharacterCount(b.LineBytes(end.Y)) - charsEnd
	return nfound, deltaX
}
