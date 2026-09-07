package cli

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDestroyCommandExposesOnlyExplicitDestructiveLifecycleFlags(t *testing.T) {
	cmd := newDestroyCmd()
	for _, name := range []string{"purge-data-storage", "reboot", "yes"} {
		flag := cmd.Flags().Lookup(name)
		require.NotNil(t, flag, "missing --%s", name)
		assert.Equal(t, "false", flag.DefValue)
	}
	assert.Contains(t, cmd.Flags().Lookup("purge-data-storage").Usage, "destructively remove")
	assert.Contains(t, cmd.Flags().Lookup("reboot").Usage, "after successful destroy")
}
