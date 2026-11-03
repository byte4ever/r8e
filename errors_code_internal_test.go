package r8e

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEverySentinelHasAUniqueCode parses errors.go and checks that every
// resilienceError literal sets a snake_case code that no other sentinel
// uses. A sentinel added without a code fails here, so [ErrorCode] and the
// trace labels built on it never fall back to a generic value for it.
func TestEverySentinelHasAUniqueCode(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "errors.go", nil, 0)
	require.NoError(t, err)

	snake := regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
	seen := map[string]bool{}

	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}

		if ident, isIdent := lit.Type.(*ast.Ident); !isIdent || ident.Name != "resilienceError" {
			return true
		}

		code := literalCode(t, lit)
		assert.Regexp(t, snake, code, "sentinel code must be snake_case")
		assert.False(t, seen[code], "sentinel code %q is used twice", code)
		seen[code] = true

		return false
	})

	require.NotEmpty(t, seen, "no resilienceError literal found in errors.go")
}

// literalCode returns the string value of the code field of a resilienceError
// literal, or "" when the literal does not set it.
func literalCode(t *testing.T, lit *ast.CompositeLit) string {
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
		require.True(t, isBasic, "sentinel code must be a string literal")

		code, err := strconv.Unquote(basic.Value)
		require.NoError(t, err)

		return code
	}

	return ""
}
