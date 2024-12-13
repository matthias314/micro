package buffer

import (
	"regexp"

	"github.com/zyedidia/micro/v2/internal/util"
)

const (
	padStart = 1 << iota
	padEnd
)

// We want "^" and "$" to match only the beginning/end of a line, not the
// beginning/end of the search region if it is in the middle of a line.
// In that case we use padded regexps to require a rune before or after
// the match. (This also affects other empty-string patters like "\\b".)
// The function padRegexp creates these padded regexps.
func padRegexp(r *regexp.Regexp) [4]*regexp.Regexp {
	rPadStart := regexp.MustCompile(".(?:" + r.String() + ")")
	rPadEnd := regexp.MustCompile("(?:" + r.String() + ").")
	rPadBoth := regexp.MustCompile(".(?:" + r.String() + ").")
	return [4]*regexp.Regexp{r, rPadStart, rPadEnd, rPadBoth}
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

func (b *Buffer) findDown(r *regexp.Regexp, start, end Loc) ([2]Loc, bool) {
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

	rPadded := padRegexp(r)

	for i := start.Y; i <= end.Y; i++ {
		l, charpos, padMode := findLineParams(b, start, end, i)

		match := rPadded[padMode].FindIndex(l)

		if match != nil {
			start := Loc{charpos + util.RunePos(l, match[0]), i}
			if padMode&padStart != 0 {
				start = start.Move(1, b)
			}
			end := Loc{charpos + util.RunePos(l, match[1]), i}
			if padMode&padEnd != 0 {
				end = end.Move(-1, b)
			}
			return [2]Loc{start, end}, true
		}
	}
	return [2]Loc{}, false
}

func (b *Buffer) findUp(r *regexp.Regexp, start, end Loc) ([2]Loc, bool) {
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

	rPadded := padRegexp(r)

	for i := end.Y; i >= start.Y; i-- {
		l, charpos, padMode := findLineParams(b, start, end, i)

		allMatches := rPadded[padMode].FindAllIndex(l, -1)

		if allMatches != nil {
			match := allMatches[len(allMatches)-1]
			start := Loc{charpos + util.RunePos(l, match[0]), i}
			if padMode&padStart != 0 {
				start = start.Move(1, b)
			}
			end := Loc{charpos + util.RunePos(l, match[1]), i}
			if padMode&padEnd != 0 {
				end = end.Move(-1, b)
			}
			return [2]Loc{start, end}, true
		}
	}
	return [2]Loc{}, false
}

// FindNext finds the next occurrence of a given string in the buffer
// It returns the start and end location of the match (if found) and
// a boolean indicating if it was found
// May also return an error if the search regex is invalid
func (b *Buffer) FindNext(s string, start, end, from Loc, down bool, useRegex bool) ([2]Loc, bool, error) {
	if s == "" {
		return [2]Loc{}, false, nil
	}

	var r *regexp.Regexp
	var err error

	if !useRegex {
		s = regexp.QuoteMeta(s)
	}

	if b.Settings["ignorecase"].(bool) {
		r, err = regexp.Compile("(?i)" + s)
	} else {
		r, err = regexp.Compile(s)
	}

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
// and returns the number of replacements made and the number of runes
// added or removed on the last line of the range
func (b *Buffer) ReplaceRegex(start, end Loc, search *regexp.Regexp, replace []byte, captureGroups bool) (int, int) {
	if start.GreaterThan(end) {
		start, end = end, start
	}

	netrunes := 0

	found := 0
	var deltas []Delta
	for i := start.Y; i <= end.Y; i++ {
		l := b.lines[i].data
		charpos := 0

		if start.Y == end.Y && i == start.Y {
			l = util.SliceStart(l, end.X)
			l = util.SliceEnd(l, start.X)
			charpos = start.X
		} else if i == start.Y {
			l = util.SliceEnd(l, start.X)
			charpos = start.X
		} else if i == end.Y {
			l = util.SliceStart(l, end.X)
		}
		newText := search.ReplaceAllFunc(l, func(in []byte) []byte {
			var result []byte
			if captureGroups {
				for _, submatches := range search.FindAllSubmatchIndex(in, -1) {
					result = search.Expand(result, replace, in, submatches)
				}
			} else {
				result = replace
			}
			found++
			if i == end.Y {
				netrunes += util.CharacterCount(result) - util.CharacterCount(in)
			}
			return result
		})

		from := Loc{charpos, i}
		to := Loc{charpos + util.CharacterCount(l), i}

		deltas = append(deltas, Delta{newText, from, to})
	}
	b.MultipleReplace(deltas)

	return found, netrunes
}
