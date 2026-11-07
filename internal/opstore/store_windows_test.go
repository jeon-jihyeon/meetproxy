//go:build windows

package opstore_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeon-jihyeon/meetproxy/internal/opstore"
	"github.com/stretchr/testify/assert"
)

func TestUnsupportedLocalJournal(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "uncreated")
	store := opstore.New(dir)
	_, _, claim := store.Claim(context.Background(), "op1", "fingerprint", []byte(`{}`))
	save := store.Save(context.Background(), "op1", "fingerprint", []byte(`{}`))
	_, _, read := store.Read(context.Background(), "op1")
	assert.ErrorIs(t, claim, opstore.ErrUnsupported)
	assert.ErrorIs(t, save, opstore.ErrUnsupported)
	assert.ErrorIs(t, read, opstore.ErrUnsupported)
	_, err := os.Stat(dir)
	assert.True(t, os.IsNotExist(err))
}
