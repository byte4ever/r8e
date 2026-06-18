package r8e

import (
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCoreImportsNoTransport enforces the doc.go promise that the core package
// imports no transport or persistence machinery and performs no serialization:
// transport lives in r8ehttp, file loading in r8econf, serialization at the
// edge. The ban covers every machinery package doc.go's claim implies absent
// (transport, raw sockets, file/db persistence, JSON). If this fails, either
// move the dependency to an edge package or revise doc.go — the claim must not
// silently become a lie.
func TestCoreImportsNoTransport(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"net/http",      // transport
		"net",           // raw sockets / transport
		"encoding/json", // serialization
		"os",            // file persistence
		"database/sql",  // db persistence
	}

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() ||
			!strings.HasSuffix(name, ".go") ||
			strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			for _, bad := range forbidden {
				require.NotEqualf(t, bad, path,
					"%s imports forbidden transport package %q (see doc.go)", name, path)
			}
		}
	}
}
