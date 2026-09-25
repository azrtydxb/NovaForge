package semanticindex

import "fmt"

// scip print --json uses encoding/json's names for protobuf oneofs. Keep these
// names aligned with the pinned converter, not with protobuf JSON's other shape.
type scipTypedRange struct {
	Single *struct {
		Line  int `json:"line"`
		Start int `json:"start_character"`
		End   int `json:"end_character"`
	} `json:"SingleLineRange"`
	Multi *struct {
		StartLine int `json:"start_line"`
		Start     int `json:"start_character"`
		EndLine   int `json:"end_line"`
		End       int `json:"end_character"`
	} `json:"MultiLineRange"`
}

func decodeSCIPRange(legacy []int, typed *scipTypedRange) (Range, error) {
	if typed == nil {
		return scipRange(legacy)
	}
	var r Range
	switch {
	case typed.Single != nil && typed.Multi == nil:
		v := typed.Single
		r = Range{Position{v.Line, v.Start}, Position{v.Line, v.End}}
	case typed.Multi != nil && typed.Single == nil:
		v := typed.Multi
		r = Range{Position{v.StartLine, v.Start}, Position{v.EndLine, v.End}}
	default:
		return r, fmt.Errorf("SCIP typed range must contain exactly one known variant")
	}
	if len(legacy) > 0 {
		old, err := scipRange(legacy)
		if err != nil || old != r {
			return r, fmt.Errorf("SCIP legacy and typed ranges disagree")
		}
	}
	return r, nil
}
