package backup_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gitea.deepak.science/deepak/taiga/internal/backup"
)

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

type files map[string][]byte

func (f files) open(_ context.Context, hash string) (io.ReadCloser, error) {
	b, ok := f[hash]
	if !ok {
		return nil, errors.New("no such file")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func sample(t *testing.T) (*backup.Archive, files) {
	t.Helper()
	a, b := []byte("first fit file"), []byte("second fit file")
	ha, hb := hashOf(a), hashOf(b)
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	name, key := "Lake loop", "route-1"
	return &backup.Archive{
		Manifest: backup.Manifest{Format: backup.Format, Version: backup.Version, ProcessorVersion: "v1", CreatedAt: now, Fetched: true},
		Files: []backup.File{
			{SHA256: ha, Timestamp: now, CreatedAt: now, Workout: true},
			{SHA256: hb, Timestamp: now, CreatedAt: now},
		},
		Corrections:    []backup.Correction{{File: ha, Sport: "walking"}},
		Stretches:      []backup.Stretch{{Name: "Hill", Sport: "running", Meters: 500, Points: json.RawMessage(`[[43,-89],[43.01,-89]]`), SourceFile: &ha, CreatedAt: now}},
		Routes:         []backup.Route{{Key: key, Name: &name, Representative: &ha, Workouts: []string{ha}, CreatedAt: now}},
		RouteOverrides: []backup.RouteOverride{{File: ha, Route: &key, CreatedAt: now}},
	}, files{ha: a, hb: b}
}

func write(t *testing.T, a *backup.Archive, f files) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, backup.Write(context.Background(), &buf, a, f.open))
	return buf.Bytes()
}

func TestWriteThenRead(t *testing.T) {
	a, f := sample(t)
	stored := files{}
	got, err := backup.Read(bytes.NewReader(write(t, a, f)), func(hash string, b []byte) error {
		stored[hash] = b
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, f, stored)
	assert.Equal(t, a.Manifest, got.Manifest)
	assert.Equal(t, a.Files, got.Files)
	assert.Equal(t, a.Corrections, got.Corrections)
	assert.Equal(t, a.Stretches, got.Stretches)
	assert.Equal(t, a.Routes, got.Routes)
	assert.Equal(t, a.RouteOverrides, got.RouteOverrides)
	assert.Empty(t, got.Weather)
}

func TestWriteRefusesAFileThatDoesntMatchItsHash(t *testing.T) {
	a, f := sample(t)
	f[a.Files[0].SHA256] = []byte("something else")
	err := backup.Write(context.Background(), io.Discard, a, f.open)
	assert.ErrorContains(t, err, "doesn't match its hash")
}

func noStore(string, []byte) error { return nil }

func TestReadRefusesANewerVersion(t *testing.T) {
	a, f := sample(t)
	a.Manifest.Version = backup.Version + 1
	_, err := backup.Read(bytes.NewReader(write(t, a, f)), noStore)
	assert.ErrorIs(t, err, backup.ErrInvalid)
	assert.ErrorContains(t, err, "upgrade")
}

func TestReadRefusesSomethingElse(t *testing.T) {
	a, f := sample(t)
	a.Manifest.Format = "not-taiga"
	_, err := backup.Read(bytes.NewReader(write(t, a, f)), noStore)
	assert.ErrorIs(t, err, backup.ErrInvalid)

	_, err = backup.Read(bytes.NewReader([]byte("plain text")), noStore)
	assert.ErrorIs(t, err, backup.ErrInvalid)
}

// entries rewrites an archive, letting edit change, drop (nil) or add to its entries.
func entries(t *testing.T, archive []byte, edit func(name string, b []byte) []byte, extra ...[2]string) []byte {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(archive))
	var out bytes.Buffer
	tw := tar.NewWriter(&out)
	put := func(name string, b []byte) {
		require.NoError(t, tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(b))}))
		_, err := tw.Write(b)
		require.NoError(t, err)
	}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		b, err := io.ReadAll(tr)
		require.NoError(t, err)
		if b = edit(hdr.Name, b); b != nil {
			put(hdr.Name, b)
		}
	}
	for _, e := range extra {
		put(e[0], []byte(e[1]))
	}
	require.NoError(t, tw.Close())
	return out.Bytes()
}

func TestReadRefusesADamagedArchive(t *testing.T) {
	a, f := sample(t)
	good := write(t, a, f)
	first := "files/" + a.Files[0].SHA256
	cases := map[string][]byte{
		"a file left out": entries(t, good, func(name string, b []byte) []byte {
			if name == first {
				return nil
			}
			return b
		}),
		"a document left out": entries(t, good, func(name string, b []byte) []byte {
			if name == "weather.json" {
				return nil
			}
			return b
		}),
		"a file changed": entries(t, good, func(name string, b []byte) []byte {
			if name == first {
				return []byte("tampered")
			}
			return b
		}),
		"an unknown field": entries(t, good, func(name string, b []byte) []byte {
			if name == "corrections.json" {
				return []byte(`[{"file":"x","colour":"red"}]`)
			}
			return b
		}),
		"an unlisted file": entries(t, good, func(_ string, b []byte) []byte { return b }, [2]string{"files/" + hashOf([]byte("extra")), "extra"}),
		"an unknown entry": entries(t, good, func(_ string, b []byte) []byte { return b }, [2]string{"notes.txt", "hi"}),
		"cut short":        good[:len(good)/2],
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := backup.Read(bytes.NewReader(archive), noStore)
			assert.ErrorIs(t, err, backup.ErrInvalid)
		})
	}
}
