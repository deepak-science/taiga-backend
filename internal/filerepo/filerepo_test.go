package filerepo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/config"
)

func newLocal(t *testing.T, prefixLength int) (FileRepo, string) {
	t.Helper()
	dir := t.TempDir()
	repo, err := NewFileRepo(config.Config{FileRepo: config.FileRepoConfig{Type: "local", AssetPath: dir, PrefixLength: prefixLength}})
	require.NoError(t, err)
	return repo, dir
}

func hashOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestLocalStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo, dir := newLocal(t, 2)
	content := []byte("a workout, more or less\x00\xff")

	hash, err := repo.Store(ctx, bytes.NewReader(content))
	require.NoError(t, err)
	assert.Equal(t, hashOf(content), hash)

	exists, err := repo.Exists(ctx, hash)
	require.NoError(t, err)
	assert.True(t, exists)

	rc, err := repo.Fetch(ctx, hash)
	require.NoError(t, err)
	got, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, content, got)

	onDisk, err := os.ReadFile(filepath.Join(dir, hash[:2], hash[2:]))
	require.NoError(t, err, "stored under a directory named by the hash's prefix")
	assert.Equal(t, content, onDisk)
}

func TestLocalStoreSameContentIsOneFile(t *testing.T) {
	ctx := context.Background()
	repo, dir := newLocal(t, 2)

	first, err := repo.Store(ctx, strings.NewReader("same"))
	require.NoError(t, err)
	second, err := repo.Store(ctx, strings.NewReader("same"))
	require.NoError(t, err)
	other, err := repo.Store(ctx, strings.NewReader("different"))
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.NotEqual(t, first, other)

	var files int
	require.NoError(t, filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			files++
		}
		return err
	}))
	assert.Equal(t, 2, files)
}

func TestLocalStoreWithoutAPrefix(t *testing.T) {
	ctx := context.Background()
	repo, dir := newLocal(t, 0)

	hash, err := repo.Store(ctx, strings.NewReader("flat"))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(dir, hash))
	assert.NoError(t, err)
}

func TestLocalStoreUnknownHash(t *testing.T) {
	ctx := context.Background()
	repo, _ := newLocal(t, 2)
	missing := hashOf([]byte("never stored"))

	exists, err := repo.Exists(ctx, missing)
	require.NoError(t, err)
	assert.False(t, exists)

	_, err = repo.Fetch(ctx, missing)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestOnlyHashesReachTheStore(t *testing.T) {
	ctx := context.Background()
	repo, dir := newLocal(t, 2)
	outside := filepath.Join(filepath.Dir(dir), "outside-"+filepath.Base(dir))
	require.NoError(t, os.WriteFile(outside, []byte("not the repo's to serve"), 0o600))
	t.Cleanup(func() { _ = os.Remove(outside) })

	for name, hash := range map[string]string{
		"traversal":               "../" + filepath.Base(outside),
		"empty":                   "",
		"shorter than the prefix": "a",
		"short":                   strings.Repeat("a", 63),
		"long":                    strings.Repeat("a", 65),
		"uppercase":               strings.Repeat("A", 64),
		"not hex":                 strings.Repeat("g", 64),
		"separator":               strings.Repeat("a", 31) + "/." + strings.Repeat("a", 31),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := repo.Fetch(ctx, hash)
			assert.ErrorIs(t, err, ErrInvalidHash)
			_, err = repo.Exists(ctx, hash)
			assert.ErrorIs(t, err, ErrInvalidHash)

			// Without a client: the check has to turn these away before S3 is asked.
			s3 := &s3Store{bucket: "b", prefixLength: 2}
			_, err = s3.Fetch(ctx, hash)
			assert.ErrorIs(t, err, ErrInvalidHash)
			_, err = s3.Exists(ctx, hash)
			assert.ErrorIs(t, err, ErrInvalidHash)
		})
	}
}

func TestUnknownRepoType(t *testing.T) {
	_, err := NewFileRepo(config.Config{FileRepo: config.FileRepoConfig{Type: "ftp"}})
	assert.ErrorContains(t, err, "ftp")
}

func TestS3Keys(t *testing.T) {
	hash := hashOf([]byte("x"))
	for name, tc := range map[string]struct {
		store s3Store
		want  string
	}{
		"prefix and fan-out": {s3Store{prefix: "files", prefixLength: 2}, "files/" + hash[:2] + "/" + hash[2:]},
		"no prefix":          {s3Store{prefixLength: 2}, hash[:2] + "/" + hash[2:]},
		"no fan-out":         {s3Store{prefix: "files"}, "files/" + hash},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.store.buildKey(hash))
		})
	}
}
