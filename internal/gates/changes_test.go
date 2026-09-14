package gates_test

import (
	"reflect"
	"testing"

	"github.com/novaforge/novaforge/internal/gates"
)

// TestChangedPathsReadsHeadersNotContent pins the one way a diff parser could
// be talked out of an approval: a hunk line that looks like a file header. A
// removed line reading "-- a/migrations/x.sql" shows in the diff as
// "--- a/migrations/x.sql" and must not be taken for a changed file — nor may
// a real migration hidden behind a rename or a quoted path be missed.
func TestChangedPathsReadsHeadersNotContent(t *testing.T) {
	unified := "diff --git a/README.md b/README.md\n" +
		"index 1111111..2222222 100644\n" +
		"--- a/README.md\n" +
		"+++ b/README.md\n" +
		"@@ -1,2 +1,1 @@\n" +
		"--- a/migrations/000009_fake.up.sql\n" +
		"+++ b/.novaforge/gates/tests.yaml\n" +
		" hello\n" +
		"diff --git a/db/old.txt b/db/migrations/000002.up.sql\n" +
		"similarity index 100%\n" +
		"rename from db/old.txt\n" +
		"rename to db/migrations/000002.up.sql\n" +
		"diff --git \"a/docs/with space.md\" \"b/docs/with space.md\"\n" +
		"new file mode 100644\n" +
		"--- /dev/null\n" +
		"+++ \"b/docs/with space.md\"\n" +
		"@@ -0,0 +1 @@\n" +
		"+x\n"

	got := gates.ChangedPaths(unified)
	want := []string{"README.md", "db/migrations/000002.up.sql", "db/old.txt", "docs/with space.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChangedPaths = %q, want %q", got, want)
	}
}
