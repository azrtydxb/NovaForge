package graph

import "testing"

func TestSemanticUnspecifiedEncodingOnlyASCII(t *testing.T) {
	for _, encoding := range []int{0, 1, 2, 3} {
		got, err := semanticByteOffset("hello", 3, encoding)
		if err != nil || got != 3 {
			t.Fatalf("encoding %d: %d %v", encoding, got, err)
		}
	}
	if _, err := semanticByteOffset("éhello", 3, 0); err == nil {
		t.Fatal("guessed encoding for non-ASCII")
	}
}
