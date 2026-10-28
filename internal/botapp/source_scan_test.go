package botapp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These tests read the package's own source rather than rendering screens.
//
// A rendering test only covers the screens somebody remembered to list, and the
// defects they exist to catch — a button whose action has no handler, a
// callback that Telegram refuses because it is longer than 64 bytes, a text key
// with no translation — were all in screens nobody listed. Reading every call
// to `actionButton`, `callbackButton`, and `text` in the package is what makes
// the check exhaustive: a new screen is covered the moment it is written.

type sourceFile struct {
	name string
	file *ast.File
}

func packageSources(t *testing.T) (*token.FileSet, []sourceFile) {
	t.Helper()
	fileSet := token.NewFileSet()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	files := make([]sourceFile, 0, len(paths))
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		parsed, parseErr := parser.ParseFile(fileSet, path, source, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		files = append(files, sourceFile{name: path, file: parsed})
	}
	return fileSet, files
}

// formatVerb matches one fmt verb, including `%%`.
var formatVerb = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)
