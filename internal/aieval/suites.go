package aieval

import (
	"embed"
	"path"
	"sort"
	"strings"
)

// The suites ship with the binary rather than living in a test directory.
//
// An installation should be able to run the evaluation against its own
// configured provider and model, because that is the combination that matters
// and it is not the one the project's CI ran. A set that only exists in the
// repository answers "did Omniflow regress?"; one that ships answers "did my
// installation regress?", which is the question an owner has.
//
//go:embed suites/*.json
var suiteFiles embed.FS

// Suites loads every embedded set, refusing the whole lot if one is unusable.
//
// All-or-nothing because a partially loaded evaluation reports a pass rate over
// the cases that happened to parse, which is a number that looks like a result
// and is not.
func Suites() (map[string]*Suite, error) {
	entries, err := suiteFiles.ReadDir("suites")
	if err != nil {
		return nil, err
	}
	loaded := make(map[string]*Suite, len(entries))
	for _, entry := range entries {
		document, err := suiteFiles.ReadFile(path.Join("suites", entry.Name()))
		if err != nil {
			return nil, err
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		suite, err := Load(name, document)
		if err != nil {
			return nil, err
		}
		loaded[name] = suite
	}
	return loaded, nil
}

// SuiteNames lists the embedded sets, sorted.
func SuiteNames() []string {
	entries, err := suiteFiles.ReadDir("suites")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, strings.TrimSuffix(entry.Name(), ".json"))
	}
	sort.Strings(names)
	return names
}
