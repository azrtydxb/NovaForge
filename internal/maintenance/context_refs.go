package maintenance

import (
	"fmt"
	"go/token"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Only explicit symbol: links are evidence. Backticks, prose and guessed
// identifier names are not declarations that a document references code.
var symbolLink = regexp.MustCompile(`\]\((symbol:[^\s)]+)\)`)

func contextReferences(dir string) ([]ContextDocRef, error) {
	root := filepath.Join(dir, ".novaforge", "context")
	var docs []ContextDocRef
	err := filepath.WalkDir(root, func(name string, entry fs.DirEntry, err error) error {
		if os.IsNotExist(err) && name == root {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".md") {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 1024*1024 || len(docs) >= 1024 {
			return fmt.Errorf("context-reference scan exceeds 1024 documents or 1 MiB/document")
		}
		body, err := os.ReadFile(name)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, name)
		if err != nil {
			return err
		}
		doc := ContextDocRef{Path: filepath.ToSlash(rel)}
		seen := map[string]bool{}
		for _, match := range symbolLink.FindAllSubmatch(body, -1) {
			u, err := url.Parse(string(match[1]))
			if err != nil {
				return fmt.Errorf("invalid symbol link in %s: %w", rel, err)
			}
			file, err := url.PathUnescape(u.Opaque)
			if err != nil || !strings.HasSuffix(file, ".go") || path.Clean(file) != file || path.IsAbs(file) || strings.HasPrefix(file, "../") || !token.IsIdentifier(u.Fragment) {
				return fmt.Errorf("invalid Go symbol link in %s", rel)
			}
			key := file + "#" + u.Fragment
			if !seen[key] {
				seen[key] = true
				doc.ReferencedSymbols = append(doc.ReferencedSymbols, key)
			}
		}
		docs = append(docs, doc)
		return nil
	})
	return docs, err
}
