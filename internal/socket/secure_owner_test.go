//go:build !windows

package socket

import (
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOwner(t *testing.T) {
	t.Parallel()
	tcs := []struct {
		name string
		diff uint32
		want bool
	}{
		{"current user", 0, true},
		{"another user", 1, false},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			info, err := os.Stat(t.TempDir())
			require.NoError(t, err)
			stat, ok := info.Sys().(*syscall.Stat_t)
			require.True(t, ok)
			stat.Uid = uint32(os.Getuid()) + tc.diff

			assert.Equal(t, tc.want, owned(info))
		})
	}
}
