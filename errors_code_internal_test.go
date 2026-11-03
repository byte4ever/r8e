package r8e

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEverySentinelHasAUniqueCode parses every non-test file of the package.
// It checks that every exported Err* variable is a resilienceError literal,
// and that every resilienceError literal sets a snake_case code no other
// sentinel uses. A sentinel added without a code, built with errors.New, or
// declared in another file fails here, so [ErrorCode] and the trace labels
// built on it never fall back to a generic value for it.
func TestEverySentinelHasAUniqueCode(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	seen := make(map[string]string)
	sentinels := 0

	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, parseErr)

		for _, spec := range exportedErrVars(file) {
			for i, ident := range spec.Names {
				pos := fset.Position(ident.Pos())

				require.Greater(t, len(spec.Values), i, "%s at %s has no value", ident.Name, pos)

				lit, ok := spec.Values[i].(*ast.CompositeLit)
				isSentinel := ok && isResilienceError(lit)
				assert.True(t, isSentinel, "%s at %s must be a resilienceError literal", ident.Name, pos)
			}
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isResilienceError(lit) {
				return true
			}

			pos := fset.Position(lit.Pos())
			code := literalCode(t, lit, pos)
			sentinels++

			assert.Regexp(t, snake, code, "sentinel code at %s must be snake_case", pos)
			assert.NotContains(t, seen, code, "sentinel code %q at %s is already used at %s", code, pos, seen[code])
			seen[code] = pos.String()

			return false
		})
	}

	require.NotZero(t, sentinels, "no resilienceError literal found in the package")
}

// exportedErrVars returns the package-level var specs of file that declare an
// exported name starting with Err.
func exportedErrVars(file *ast.File) []*ast.ValueSpec {
	var specs []*ast.ValueSpec

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}

		for _, spec := range gen.Specs {
			value, isValue := spec.(*ast.ValueSpec)
			if isValue && hasErrName(value) {
				specs = append(specs, value)
			}
		}
	}

	return specs
}

// hasErrName reports whether spec declares a name that starts with Err.
func hasErrName(spec *ast.ValueSpec) bool {
	for _, ident := range spec.Names {
		if strings.HasPrefix(ident.Name, "Err") {
			return true
		}
	}

	return false
}

// isResilienceError reports whether lit is a resilienceError literal.
func isResilienceError(lit *ast.CompositeLit) bool {
	ident, ok := lit.Type.(*ast.Ident)

	return ok && ident.Name == "resilienceError"
}

// literalCode returns the string value of the code field of a resilienceError
// literal, or "" when the literal does not set it.
func literalCode(t *testing.T, lit *ast.CompositeLit, pos token.Position) string {
	t.Helper()

	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}

		key, isIdent := kv.Key.(*ast.Ident)
		if !isIdent || key.Name != "code" {
			continue
		}

		basic, isBasic := kv.Value.(*ast.BasicLit)
		require.True(t, isBasic, "sentinel code at %s must be a string literal", pos)

		code, err := strconv.Unquote(basic.Value)
		require.NoError(t, err)

		return code
	}

	return ""
}
