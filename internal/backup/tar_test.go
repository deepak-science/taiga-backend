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
		Manifest: backup.Manifest{Format: backup.Format, Kind: backup.KindUser, Version: backup.Version, ProcessorVersion: "v1", CreatedAt: now, Fetched: true},
		Data: backup.Data{Files: []backup.File{
			{SHA256: ha, Timestamp: now, CreatedAt: now, Workout: true},
			{SHA256: hb, Timestamp: now, CreatedAt: now},
		},
			Corrections:    []backup.Correction{{File: ha, Sport: "walking"}},
			Stretches:      []backup.Stretch{{Name: "Hill", Sport: "running", Meters: 500, Points: json.RawMessage(`[[43,-89],[43.01,-89]]`), SourceFile: &ha, CreatedAt: now}},
			Routes:         []backup.Route{{Key: key, Name: &name, Representative: &ha, Workouts: []string{ha}, CreatedAt: now}},
			RouteOverrides: []backup.RouteOverride{{File: ha, Route: &key, CreatedAt: now}},
		},
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
	got, err := backup.Read(bytes.NewReader(write(t, a, f)), func(hash string, r io.Reader) error {
		b, err := io.ReadAll(r)
		require.NoError(t, err)
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

func noStore(string, io.Reader) error { return nil }

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
		"more after a document": entries(t, good, func(name string, b []byte) []byte {
			if name == "workouts.json" {
				return []byte(`[] {"unexpected":true}`)
			}
			return b
		}),
		"cut short": good[:len(good)/2],
	}
	for name, archive := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := backup.Read(bytes.NewReader(archive), noStore)
			assert.ErrorIs(t, err, backup.ErrInvalid)
		})
	}
}

func TestADeploymentsAccountsShareTheirFiles(t *testing.T) {
	one, f := sample(t)
	c := []byte("carol's own file")
	f[hashOf(c)] = c
	now := one.Manifest.CreatedAt
	a := &backup.Archive{
		Manifest: backup.Manifest{Format: backup.Format, Kind: backup.KindDeployment, Version: backup.Version, CreatedAt: now},
		Accounts: []backup.Account{
			{Key: "account-1", Email: "alice@example.com", Password: []byte("$2a$10$hash"), EmailVerified: true, CreatedAt: now, Identities: []backup.Identity{{Issuer: "https://auth.example.com/", Subject: "a1", CreatedAt: now}}, Data: one.Data},
			{Key: "account-2", Email: "carol@example.com", CreatedAt: now, Identities: []backup.Identity{}, Data: backup.Data{Files: append([]backup.File{one.Files[0]}, backup.File{SHA256: hashOf(c), Timestamp: now, CreatedAt: now})}},
		},
	}
	stored := map[string]int{}
	archive := write(t, a, f)
	got, err := backup.Read(bytes.NewReader(archive), func(hash string, _ io.Reader) error {
		stored[hash]++
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, map[string]int{one.Files[0].SHA256: 1, one.Files[1].SHA256: 1, hashOf(c): 1}, stored, "a file two accounts have is in it once")
	require.Len(t, got.Accounts, 2)
	assert.Equal(t, a.Accounts[0].Password, got.Accounts[0].Password)
	assert.Equal(t, a.Accounts[0].Identities, got.Accounts[0].Identities)
	assert.Equal(t, one.Routes, got.Accounts[0].Routes)
	assert.Equal(t, a.Accounts[1].Files, got.Accounts[1].Files)
	assert.Empty(t, got.Files, "nothing at the top")

	t.Run("an account's document left out", func(t *testing.T) {
		damaged := entries(t, archive, func(name string, b []byte) []byte {
			if name == "accounts/account-2/stretches.json" {
				return nil
			}
			return b
		})
		_, err := backup.Read(bytes.NewReader(damaged), noStore)
		assert.ErrorIs(t, err, backup.ErrInvalid)
	})

	t.Run("a key that isn't a name", func(t *testing.T) {
		a.Accounts[1].Key = "../etc"
		assert.Error(t, backup.Write(context.Background(), io.Discard, a, f.open))
	})
}

// pattern is size bytes, made as they're read, so a test can stream a file bigger than it should hold.
type pattern struct{ left int64 }

func (p *pattern) Read(b []byte) (int, error) {
	if p.left == 0 {
		return 0, io.EOF
	}
	n := int64(len(b))
	if n > p.left {
		n = p.left
	}
	for i := range b[:n] {
		b[i] = byte(i % 251)
	}
	p.left -= n
	return int(n), nil
}

func TestAFileBiggerThanAnUploadStreamsThrough(t *testing.T) {
	const size = 70 << 20
	h := sha256.New()
	_, err := io.Copy(h, &pattern{left: size})
	require.NoError(t, err)
	big := hex.EncodeToString(h.Sum(nil))
	open := func(context.Context, string) (io.ReadCloser, error) {
		return io.NopCloser(&pattern{left: size}), nil
	}
	a := &backup.Archive{
		Manifest: backup.Manifest{Format: backup.Format, Kind: backup.KindUser, Version: backup.Version},
		Data:     backup.Data{Files: []backup.File{{SHA256: big, Workout: true}}},
	}

	pr, pw := io.Pipe()
	go func() { _ = pw.CloseWithError(backup.Write(context.Background(), pw, a, open)) }()
	var got int64
	_, err = backup.Read(pr, func(hash string, r io.Reader) error {
		n, err := io.Copy(io.Discard, r)
		got = n
		return err
	})
	require.NoError(t, err)
	assert.Equal(t, int64(size), got)
}

func TestWriteRefusesAFileThatChangesWhileItsRead(t *testing.T) {
	a, f := sample(t)
	reads := 0
	open := func(ctx context.Context, hash string) (io.ReadCloser, error) {
		reads++
		if reads == 2 {
			return io.NopCloser(bytes.NewReader([]byte("first fit filX"))), nil
		}
		return f.open(ctx, hash)
	}
	assert.Error(t, backup.Write(context.Background(), io.Discard, a, open))
}
