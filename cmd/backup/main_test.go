package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "taiga.tar")
	require.NoError(t, os.WriteFile(path, []byte("last night's"), 0o600))

	err := writeFile(path, func(w io.Writer) error {
		_, _ = w.Write([]byte("half"))
		return errors.New("the database went away")
	})
	require.Error(t, err)
	got, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "last night's", string(got), "a failed backup leaves the last one alone")

	require.NoError(t, writeFile(path, func(w io.Writer) error {
		_, err := w.Write([]byte("tonight's"))
		return err
	}))
	got, err = os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "tonight's", string(got))
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "it holds password hashes")

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no partial file left behind")
}
