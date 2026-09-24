// Package archtest enforces the dependency rule and the no-float rule for money.
package archtest

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/lavarini/backend-challenge-go"

// allowed lists, per restricted package prefix, the module-internal prefixes it
// may import. Restricted packages may import the standard library and nothing
// external. Unlisted packages are unrestricted.
var allowed = map[string][]string{
	"internal/money":    {},
	"internal/wallet":   {"internal/money"},
	"internal/wagering": {"internal/money"},
	"internal/events":   {"internal/money"},
	"internal/app":      {"internal/money", "internal/wallet", "internal/wagering", "internal/events"},
}

// noFloat lists package prefixes where float32/float64 must never appear.
var noFloat = []string{
	"internal/money", "internal/wallet", "internal/wagering", "internal/events",
	"internal/app", "internal/adapters",
}

type listedPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	Imports    []string
}

func TestDependencyRule(t *testing.T) {
	for _, p := range listPackages(t) {
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, module), "/")
		prefix, restricted := ruleFor(rel)
		if !restricted {
			continue
		}
		for _, imp := range p.Imports {
			if isStdlib(imp) {
				continue
			}
			if !strings.HasPrefix(imp, module+"/") {
				t.Errorf("%s imports external package %s", rel, imp)
				continue
			}
			target := strings.TrimPrefix(imp, module+"/")
			if hasPrefix(target, prefix) {
				continue
			}
			ok := false
			for _, a := range allowed[prefix] {
				if hasPrefix(target, a) {
					ok = true
					break
				}
			}
			if !ok {
				t.Errorf("%s must not import %s", rel, target)
			}
		}
	}
}

func TestNoFloatInMoneyPaths(t *testing.T) {
	for _, p := range listPackages(t) {
		rel := strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, module), "/")
		if !inAny(rel, noFloat) {
			continue
		}
		for _, f := range p.GoFiles {
			path := filepath.Join(p.Dir, f)
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && (id.Name == "float32" || id.Name == "float64") {
					t.Errorf("%s uses %s", path, id.Name)
				}
				return true
			})
		}
	}
}

func ruleFor(rel string) (string, bool) {
	for prefix := range allowed {
		if hasPrefix(rel, prefix) {
			return prefix, true
		}
	}
	return "", false
}

func inAny(rel string, prefixes []string) bool {
	for _, p := range prefixes {
		if hasPrefix(rel, p) {
			return true
		}
	}
	return false
}

func hasPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func isStdlib(importPath string) bool {
	first, _, _ := strings.Cut(importPath, "/")
	return !strings.Contains(first, ".")
}

func listPackages(t *testing.T) []listedPackage {
	t.Helper()
	cmd := exec.Command("go", "list", "-json", "./...")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var pkgs []listedPackage
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p listedPackage
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	return pkgs
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
