package operation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/MeowSalty/LinguaFlow/backend/internal/credential"
)

type Artifact struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func Hash(ctx context.Context, path string) (Artifact, error) {
	if err := ValidatePath(path, false); err != nil {
		return Artifact{}, err
	}
	before, err := os.Stat(path)
	if err != nil {
		return Artifact{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return Artifact{}, Fail(BaselineChanged, "artifact path identity changed")
	}
	h := sha256.New()
	n, err := io.Copy(h, &contextReader{ctx: ctx, reader: f})
	if err != nil {
		return Artifact{}, err
	}
	after, err := f.Stat()
	if err != nil || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) || n != opened.Size() {
		return Artifact{}, Fail(BaselineChanged, "artifact bytes changed while reading")
	}
	final, err := os.Stat(path)
	if err != nil || !os.SameFile(opened, final) {
		return Artifact{}, Fail(BaselineChanged, "artifact path changed while reading")
	}
	return Artifact{Name: filepath.Base(path), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// Copy publishes a fully synced private independent copy without overwriting.
// The caller owns the destination directory and records its identity separately.
func Copy(ctx context.Context, source, destination string) (Artifact, error) {
	if err := ValidatePath(source, false); err != nil {
		return Artifact{}, err
	}
	before, err := os.Stat(source)
	if err != nil {
		return Artifact{}, err
	}
	f, err := os.Open(source)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return Artifact{}, Fail(BaselineChanged, "copy source identity changed")
	}
	proof, err := Publish(ctx, destination, f)
	if err != nil {
		return Artifact{}, err
	}
	after, err := os.Stat(source)
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) || proof.Size != opened.Size() {
		return Artifact{}, Fail(BaselineChanged, "copy source changed while reading")
	}
	return proof, nil
}

func Publish(ctx context.Context, destination string, reader io.Reader) (Artifact, error) {
	if err := ValidatePath(filepath.Dir(destination), true); err != nil {
		return Artifact{}, err
	}
	id, err := NewID()
	if err != nil {
		return Artifact{}, err
	}
	temporary := filepath.Join(filepath.Dir(destination), ".copy-"+id)
	ok, err := credential.PublishPrivateFile(temporary, nil)
	if err != nil {
		return Artifact{}, err
	}
	if !ok {
		return Artifact{}, Fail(Incompatible, "private temporary file exists")
	}
	defer os.Remove(temporary)
	f, err := os.OpenFile(temporary, os.O_WRONLY, 0)
	if err != nil {
		return Artifact{}, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), &contextReader{ctx: ctx, reader: reader})
	err = errors.Join(copyErr, f.Sync(), f.Close())
	if err != nil {
		return Artifact{}, err
	}
	if err := renameNoReplace(temporary, destination); err != nil {
		return Artifact{}, err
	}
	if err := syncDir(filepath.Dir(destination)); err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: filepath.Base(destination), Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
