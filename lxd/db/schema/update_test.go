package schema_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/canonical/lxd/lxd/db/schema"
	"github.com/canonical/lxd/shared"
)

// A Go source file matching the given prefix is created in the calling
// package.
func TestDotGo(t *testing.T) {
	updates := map[int]schema.Update{
		1: updateCreateTable,
		2: updateInsertValue,
	}

	require.NoError(t, schema.DotGo(updates, "xyz", "xyz.go"))
	require.Equal(t, true, shared.PathExists("xyz.go"))
	require.NoError(t, os.Remove("xyz.go"))
}
