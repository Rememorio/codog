package doclint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalPackagesHavePackageDocs(t *testing.T) {
	root := repoRoot(t)
	for _, pkg := range listInternalPackages(t, root) {
		t.Run(pkg, func(t *testing.T) {
			dir := filepath.Join(root, filepath.FromSlash(pkg))
			requirePackageDoc(t, pkg, parsePackageFiles(t, dir))
		})
	}
}

func TestCommandsHaveCommandDocs(t *testing.T) {
	root := repoRoot(t)
	for _, cmd := range listCommandPackages(t, root) {
		t.Run(cmd, func(t *testing.T) {
			dir := filepath.Join(root, filepath.FromSlash(cmd))
			requireCommandDoc(t, cmd, parsePackageFiles(t, dir))
		})
	}
}

func TestInternalPackagesHaveTests(t *testing.T) {
	root := repoRoot(t)
	for _, pkg := range listInternalPackages(t, root) {
		t.Run(pkg, func(t *testing.T) {
			dir := filepath.Join(root, filepath.FromSlash(pkg))
			requirePackageTests(t, pkg, dir)
		})
	}
}

func TestSelectedInternalPackagesHaveGoDocComments(t *testing.T) {
	root := repoRoot(t)
	packages := []string{
		"internal/autofixpr",
		"internal/codeintel",
		"internal/control",
		"internal/frontmatter",
		"internal/githubcomments",
		"internal/harness",
		"internal/mcp",
		"internal/mcpserver",
		"internal/mockanthropic",
		"internal/onboarding",
		"internal/planmode",
		"internal/remote",
		"internal/signing",
		"internal/usage",
		"internal/webaccess",
		"internal/workerstate",
	}

	for _, pkg := range packages {
		t.Run(pkg, func(t *testing.T) {
			dir := filepath.Join(root, filepath.FromSlash(pkg))
			files := parsePackageFiles(t, dir)
			requirePackageDoc(t, pkg, files)
			requireExportedDocComments(t, pkg, files)
		})
	}
}

func TestReadmeStatesCompatibilityBoundaries(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "README.md"))
	require.NoError(t, err)
	readme := string(data)
	lower := strings.ToLower(readme)

	for _, required := range []string{
		"experimental Go-native coding agent",
		"not an Anthropic product",
		"not yet a polished drop-in replacement",
		"not a complete security sandbox",
	} {
		require.Contains(t, readme, required)
	}

	for _, disallowed := range []string{
		"full parity",
		"complete parity",
		"feature complete",
		"generated with",
		"made with",
		"built with cursor",
		"gocache=",
	} {
		require.NotContains(t, lower, disallowed)
	}
	require.NotRegexp(t, regexp.MustCompile(`/Users/[A-Za-z0-9._-]+/`), readme)
}

func listInternalPackages(t *testing.T, root string) []string {
	t.Helper()
	internalRoot := filepath.Join(root, "internal")
	packages := []string{}
	err := filepath.WalkDir(internalRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		files, err := parsePackageFilesAllowEmpty(path)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return nil
		}
		name := files[0].Name.Name
		if name == "main" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		packages = append(packages, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, packages)
	return packages
}

func listCommandPackages(t *testing.T, root string) []string {
	t.Helper()
	cmdRoot := filepath.Join(root, "cmd")
	packages := []string{}
	err := filepath.WalkDir(cmdRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		files, err := parsePackageFilesAllowEmpty(path)
		if err != nil {
			return err
		}
		if len(files) == 0 {
			return nil
		}
		name := files[0].Name.Name
		if name != "main" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		packages = append(packages, filepath.ToSlash(rel))
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, packages)
	return packages
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "go.mod not found from %s", dir)
		dir = parent
	}
}

func parsePackageFiles(t *testing.T, dir string) []*ast.File {
	t.Helper()
	files, err := parsePackageFilesAllowEmpty(dir)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	return files
}

func parsePackageFilesAllowEmpty(dir string) ([]*ast.File, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	files := []*ast.File{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func requirePackageDoc(t *testing.T, pkg string, files []*ast.File) {
	t.Helper()
	for _, file := range files {
		if file.Doc != nil && strings.HasPrefix(strings.TrimSpace(file.Doc.Text()), "Package ") {
			return
		}
	}
	t.Fatalf("%s is missing a package doc comment", pkg)
}

func requireCommandDoc(t *testing.T, pkg string, files []*ast.File) {
	t.Helper()
	commandName := filepath.Base(filepath.FromSlash(pkg))
	for _, file := range files {
		if file.Doc != nil && strings.HasPrefix(strings.TrimSpace(file.Doc.Text()), "Command "+commandName+" ") {
			return
		}
	}
	t.Fatalf("%s is missing a command doc comment", pkg)
}

func requirePackageTests(t *testing.T, pkg string, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), "_test.go") {
			return
		}
	}
	t.Fatalf("%s is missing a Go test file", pkg)
}

func requireExportedDocComments(t *testing.T, pkg string, files []*ast.File) {
	t.Helper()
	for _, file := range files {
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				if decl.Name.IsExported() {
					requireNamedDoc(t, pkg, decl.Name.Name, decl.Doc)
				}
			case *ast.GenDecl:
				for _, spec := range decl.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						if spec.Name.IsExported() {
							requireNamedDoc(t, pkg, spec.Name.Name, firstDoc(spec.Doc, decl.Doc))
						}
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							if name.IsExported() {
								requireNamedDoc(t, pkg, name.Name, firstDoc(spec.Doc, decl.Doc))
							}
						}
					}
				}
			}
		}
	}
}

func firstDoc(docs ...*ast.CommentGroup) *ast.CommentGroup {
	for _, doc := range docs {
		if doc != nil {
			return doc
		}
	}
	return nil
}

func requireNamedDoc(t *testing.T, pkg string, name string, doc *ast.CommentGroup) {
	t.Helper()
	require.NotNil(t, doc, "%s.%s is missing a doc comment", pkg, name)
	text := strings.TrimSpace(doc.Text())
	require.Truef(t, strings.HasPrefix(text, name+" ") || strings.HasPrefix(text, name+" is ") || strings.HasPrefix(text, name+" returns "), "%s.%s doc comment should start with the identifier name", pkg, name)
}
