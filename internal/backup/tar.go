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

// MaxFileSize bounds one file's bytes, which are held in memory while their hash is checked; uploads stop at 32MB.
const MaxFileSize = 64 << 20

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
		b, err := readFile(ctx, open, f.SHA256)
		if err != nil {
			return err
		}
		if err := writeEntry(tw, filesDir+f.SHA256, b, modTime); err != nil {
			return err
		}
	}
	return tw.Close()
}

// readFile is the file's bytes, whole, since a tar header states its size before them.
func readFile(ctx context.Context, open Opener, hash string) ([]byte, error) {
	rc, err := open(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("opening file %s: %w", hash, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading file %s: %w", hash, err)
	}
	if len(b) > MaxFileSize {
		return nil, fmt.Errorf("file %s is over %d bytes", hash, MaxFileSize)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != hash {
		return nil, fmt.Errorf("file %s doesn't match its hash", hash)
	}
	return b, nil
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
// Each file is checked against its hash, then handed to store; the documents all come first, so a.Files is complete by the first call.
// An archive cut short is an error, as is any entry that isn't one of the above.
func Read(r io.Reader, store func(hash string, content []byte) error) (*Archive, error) {
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
		if hdr.Size > MaxFileSize {
			return nil, fmt.Errorf("%w: file %s is over %d bytes", ErrInvalid, hash, MaxFileSize)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("%w: reading file %s: %w", ErrInvalid, hash, err)
		}
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != hash {
			return nil, fmt.Errorf("%w: file %s doesn't match its hash", ErrInvalid, hash)
		}
		got[hash] = true
		if err := store(hash, b); err != nil {
			return nil, err
		}
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
