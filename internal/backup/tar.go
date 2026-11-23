package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"

	"gitea.deepak.science/deepak/taiga/internal/filerepo"
)

const (
	manifestName = "manifest.json"
	accountsName = "accounts.json"
	filesDir     = "files/"
)

// maxDocumentSize bounds a JSON entry, which is decoded whole.
const maxDocumentSize = 512 << 20

// ErrInvalid is wrapped by every error Read returns for an archive that isn't a whole, readable taiga backup.
var ErrInvalid = errors.New("not a valid taiga backup")

// Opener gives a file's bytes by hash.
type Opener func(ctx context.Context, hash string) (io.ReadCloser, error)

// Write streams a as a tar: the manifest, each document, then every file's bytes as files/<sha256>.
// A file whose bytes don't match its hash is an error, so a damaged repo can't make a backup that won't restore.
func Write(ctx context.Context, w io.Writer, a *Archive, open Opener) error {
	tw := tar.NewWriter(w)
	modTime := a.Manifest.CreatedAt
	writeJSON := func(name string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encoding %s: %w", name, err)
		}
		return writeEntry(tw, name, b, modTime)
	}
	if err := checkKind(a); err != nil {
		return err
	}
	if err := writeJSON(manifestName, a.Manifest); err != nil {
		return err
	}
	if a.Manifest.Kind == KindDeployment {
		if err := writeJSON(accountsName, a.Accounts); err != nil {
			return err
		}
	}
	for _, d := range a.documents() {
		if err := writeJSON(d.name, d.v); err != nil {
			return err
		}
	}
	for _, f := range a.AllFiles() {
		if err := writeFile(ctx, tw, open, f.SHA256, modTime); err != nil {
			return err
		}
	}
	return tw.Close()
}

// writeFile streams one file into the tar: a tar header states the size first, so it's read once to measure and check it, then again to copy, checked again.
func writeFile(ctx context.Context, tw *tar.Writer, open Opener, hash string, modTime time.Time) error {
	size, err := measure(ctx, open, hash, io.Discard)
	if err != nil {
		return err
	}
	name := filesDir + hash
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size, ModTime: modTime, Format: tar.FormatPAX}); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	// A size or hash that changed between the two reads fails here (tar refuses a short or long entry), never as a quietly wrong archive.
	if _, err := measure(ctx, open, hash, tw); err != nil {
		return err
	}
	return nil
}

// measure copies the file to w, and is its size once its bytes are known to match its hash.
func measure(ctx context.Context, open Opener, hash string, w io.Writer) (int64, error) {
	rc, err := open(ctx, hash)
	if err != nil {
		return 0, fmt.Errorf("opening file %s: %w", hash, err)
	}
	defer func() { _ = rc.Close() }()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), rc)
	if err != nil {
		return 0, fmt.Errorf("reading file %s: %w", hash, err)
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		return 0, fmt.Errorf("file %s doesn't match its hash", hash)
	}
	return n, nil
}

// hashingReader hashes what's read through it, and keeps the first read error, so one in the archive isn't mistaken for the store's.
type hashingReader struct {
	r   io.Reader
	h   hash.Hash
	err error
}

func (hr *hashingReader) Read(p []byte) (int, error) {
	n, err := hr.r.Read(p)
	hr.h.Write(p[:n])
	if err != nil && !errors.Is(err, io.EOF) && hr.err == nil {
		hr.err = err
	}
	return n, err
}

func writeEntry(tw *tar.Writer, name string, b []byte, modTime time.Time) error {
	hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(b)), ModTime: modTime, Format: tar.FormatPAX}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	if _, err := tw.Write(b); err != nil {
		return fmt.Errorf("writing %s: %w", name, err)
	}
	return nil
}

// Read reads an archive Write made, of this version or an older one.
// Each file is streamed to store, and checked against its hash once read: store sees the bytes before the check, so it must be content-addressed (as a file repo is) or wait for Read to return.
// The documents all come first, so a.Files is complete by the first call.
// An archive cut short is an error, as is any entry that isn't one of the above.
func Read(r io.Reader, store func(hash string, content io.Reader) error) (*Archive, error) {
	tr := tar.NewReader(r)
	next := func() (*tar.Header, error) {
		hdr, err := tr.Next()
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		return hdr, err
	}

	hdr, err := next()
	if errors.Is(err, io.EOF) || (err == nil && hdr.Name != manifestName) {
		return nil, fmt.Errorf("%w: it doesn't start with %s", ErrInvalid, manifestName)
	}
	if err != nil {
		return nil, err
	}
	a := &Archive{}
	if err := readJSON(tr, hdr, &a.Manifest); err != nil {
		return nil, err
	}
	if a.Manifest.Format != Format {
		return nil, fmt.Errorf("%w: format %q", ErrInvalid, a.Manifest.Format)
	}
	if a.Manifest.Version < 1 {
		return nil, fmt.Errorf("%w: version %d", ErrInvalid, a.Manifest.Version)
	}
	if a.Manifest.Version > Version {
		return nil, fmt.Errorf("%w: it's format version %d, and this taiga reads up to %d; upgrade it first", ErrInvalid, a.Manifest.Version, Version)
	}
	if a.Manifest.Kind == KindDeployment {
		hdr, err := next()
		if errors.Is(err, io.EOF) || (err == nil && hdr.Name != accountsName) {
			return nil, fmt.Errorf("%w: a deployment's backup has %s after its manifest", ErrInvalid, accountsName)
		}
		if err != nil {
			return nil, err
		}
		if err := readJSON(tr, hdr, &a.Accounts); err != nil {
			return nil, err
		}
	}
	if err := checkKind(a); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	docs := map[string]any{}
	for _, d := range a.documents() {
		docs[d.name] = d.v
	}
	listed := map[string]bool{}
	got := map[string]bool{}
	for {
		hdr, err := next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if v, ok := docs[hdr.Name]; ok {
			if len(listed) > 0 || len(got) > 0 {
				return nil, fmt.Errorf("%w: %s comes after the files", ErrInvalid, hdr.Name)
			}
			if err := readJSON(tr, hdr, v); err != nil {
				return nil, err
			}
			delete(docs, hdr.Name)
			continue
		}
		hash, ok := strings.CutPrefix(hdr.Name, filesDir)
		if !ok || hdr.Typeflag != tar.TypeReg {
			return nil, fmt.Errorf("%w: unexpected entry %q", ErrInvalid, hdr.Name)
		}
		if len(docs) > 0 {
			return nil, fmt.Errorf("%w: files start before %s", ErrInvalid, missing(a, docs))
		}
		if len(listed) == 0 {
			for _, f := range a.AllFiles() {
				listed[f.SHA256] = true
			}
		}
		if filerepo.CheckHash(hash) != nil || !listed[hash] || got[hash] {
			return nil, fmt.Errorf("%w: unexpected file %q", ErrInvalid, hdr.Name)
		}
		hr := &hashingReader{r: tr, h: sha256.New()}
		storeErr := store(hash, hr)
		if hr.err != nil {
			return nil, fmt.Errorf("%w: reading file %s: %w", ErrInvalid, hash, hr.err)
		}
		if storeErr != nil {
			return nil, storeErr
		}
		// Whatever store left unread still counts toward the hash.
		if _, err := io.Copy(io.Discard, hr); err != nil {
			return nil, fmt.Errorf("%w: reading file %s: %w", ErrInvalid, hash, err)
		}
		if hex.EncodeToString(hr.h.Sum(nil)) != hash {
			return nil, fmt.Errorf("%w: file %s doesn't match its hash", ErrInvalid, hash)
		}
		got[hash] = true
	}
	if len(docs) > 0 {
		return nil, fmt.Errorf("%w: it has no %s", ErrInvalid, missing(a, docs))
	}
	for _, f := range a.AllFiles() {
		if !got[f.SHA256] {
			return nil, fmt.Errorf("%w: file %s is listed but not there; the archive may be cut short", ErrInvalid, f.SHA256)
		}
	}
	return a, nil
}

func readJSON(tr *tar.Reader, hdr *tar.Header, v any) error {
	if hdr.Size > maxDocumentSize {
		return fmt.Errorf("%w: %s is over %d bytes", ErrInvalid, hdr.Name, maxDocumentSize)
	}
	b, err := io.ReadAll(tr)
	if err != nil {
		return fmt.Errorf("%w: reading %s: %w", ErrInvalid, hdr.Name, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: reading %s: %w", ErrInvalid, hdr.Name, err)
	}
	if err := dec.Decode(&json.RawMessage{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: %s has more after its value", ErrInvalid, hdr.Name)
	}
	return nil
}

// missing names one document still to come, for an error.
func missing(a *Archive, docs map[string]any) string {
	for _, d := range a.documents() {
		if _, ok := docs[d.name]; ok {
			return d.name
		}
	}
	return ""
}

// checkKind is whether the archive's kind is one there is, and a deployment's account keys can name directories.
func checkKind(a *Archive) error {
	switch a.Manifest.Kind {
	case KindUser:
		if len(a.Accounts) > 0 {
			return errors.New("a user's export has no accounts")
		}
		return nil
	case KindDeployment:
	default:
		return fmt.Errorf("kind %q", a.Manifest.Kind)
	}
	keys := map[string]bool{}
	for _, acct := range a.Accounts {
		if !validKey(acct.Key) || keys[acct.Key] {
			return fmt.Errorf("account key %q", acct.Key)
		}
		keys[acct.Key] = true
	}
	return nil
}

func validKey(k string) bool {
	if k == "" || len(k) > 64 {
		return false
	}
	for _, c := range k {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}
