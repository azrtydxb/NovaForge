package semanticindex

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// UnmarshalJSON refuses omitted position fields: an empty object must not become
// a plausible definition at the start of a file through Go's zero values.
func (r *Range) UnmarshalJSON(b []byte) error {
	type position struct {
		Line      *int `json:"line"`
		Character *int `json:"character"`
	}
	var raw struct {
		Start *position `json:"start"`
		End   *position `json:"end"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw.Start == nil || raw.End == nil || raw.Start.Line == nil || raw.Start.Character == nil || raw.End.Line == nil || raw.End.Character == nil {
		return fmt.Errorf("range lacks explicit start/end positions")
	}
	*r = Range{Position{*raw.Start.Line, *raw.Start.Character}, Position{*raw.End.Line, *raw.End.Character}}
	return nil
}

func validRange(lines []string, r Range, encoding int) bool {
	return validPosition(lines, r.Start, encoding) && validPosition(lines, r.End, encoding) &&
		(r.End.Line > r.Start.Line || (r.End.Line == r.Start.Line && r.End.Character >= r.Start.Character))
}

func validPosition(lines []string, p Position, encoding int) bool {
	if p.Line < 0 || p.Line >= len(lines) || p.Character < 0 {
		return false
	}
	line := strings.TrimSuffix(lines[p.Line], "\r")
	if p.Character > len(line) {
		return false // every supported code-unit count is at most the byte count
	}
	if encoding == 0 {
		return true // unspecified: only bounds are known, never guess UTF-8/16
	}
	if encoding == 1 {
		return p.Character == len(line) || utf8.RuneStart(line[p.Character])
	}
	if encoding != 2 && encoding != 3 {
		return false
	}
	n := 0
	for _, r := range line {
		if n == p.Character {
			return true
		}
		n++
		if encoding == 2 && r > 0xffff {
			n++
		}
		if n > p.Character {
			return false // inside a UTF-16 surrogate pair
		}
	}
	return n == p.Character
}
