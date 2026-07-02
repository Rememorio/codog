package doclint

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSelectedInternalPackagesHaveGoDocComments(t *testing.T) {
	root := repoRoot(t)
	packages := []string{
		"internal/autofixpr",
		"internal/codeintel",
		"internal/control",
		"internal/githubcomments",
		"internal/mcp",
		"internal/mcpserver",
		"internal/onboarding",
		"internal/planmode",
		"internal/remote",
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
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	fset := token.NewFileSet()
	files := []*ast.File{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		require.NoError(t, err)
		files = append(files, file)
	}
	require.NotEmpty(t, files)
	return files
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
