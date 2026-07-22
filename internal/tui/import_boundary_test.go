package tui

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCharmImportsStayInsideTUI(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..")
	for _, directory := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repositoryRoot, directory), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			relativePath, err := filepath.Rel(repositoryRoot, path)
			if err != nil {
				return err
			}
			if strings.HasPrefix(relativePath, filepath.Join("internal", "tui")+string(filepath.Separator)) {
				return nil
			}

			file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			for _, imported := range file.Imports {
				importPath, err := strconv.Unquote(imported.Path.Value)
				if err != nil {
					return err
				}
				if strings.HasPrefix(importPath, "charm.land/bubbletea/") || strings.HasPrefix(importPath, "charm.land/lipgloss/") {
					t.Errorf("%s imports %s outside internal/tui", relativePath, importPath)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan production Go files under %s: %v", directory, err)
		}
	}
}
