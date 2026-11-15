package embed

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Bundle is a published model: what it is called, where it is, and the bytes
// it has to be.
type Bundle struct {
	Name   string
	URL    string
	SHA256 string
	Size   int64
}

// SigLIP2 is the model this build reads photographs with, a release asset
// built by build/embed/model, as the demo's media are.
var SigLIP2 = Bundle{
	Name:   "siglip2-base-patch16-224-int8.1",
	URL:    "https://github.com/stratus-private-cloud/stratus-backend/releases/download/model-siglip2-v1/siglip2-base-patch16-224-int8.1.tar.gz",
	SHA256: "87f5b053907e9692f7080c58bf6af39a9392512ef008503e35c539e98cbac9c3",
	Size:   347239358,
}

// maxBundleFile bounds what one file of an archive may unpack to.
const maxBundleFile = 1 << 30

// bundleFiles are the only names an archive may put in the directory.
var bundleFiles = map[string]bool{
	"vision.ort": true, "text.ort": true, "tokenizer.bin": true, "NAME": true, "LICENSE": true, "NOTICE": true,
}

// Fetch puts b in dir unless dir already holds it, downloading to a file
// beside it and moving it into place only once every byte is the one pinned:
// a download cut short or a mirror serving something else leaves dir as it was.
func Fetch(ctx context.Context, client *http.Client, b Bundle, dir string) error {
	name, err := os.ReadFile(filepath.Join(dir, "NAME")) //nolint:gosec // the model directory this server was given
	if err == nil && strings.TrimSpace(string(name)) == b.Name {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return fmt.Errorf("embed: fetch %s: %w", b.Name, err)
	}

	archive := dir + ".download"
	defer func() { _ = os.Remove(archive) }()
	if err := download(ctx, client, b, archive); err != nil {
		return fmt.Errorf("embed: fetch %s: %w", b.Name, err)
	}

	staged := dir + ".staged"
	if err := os.RemoveAll(staged); err != nil {
		return fmt.Errorf("embed: fetch %s: %w", b.Name, err)
	}
	defer func() { _ = os.RemoveAll(staged) }()
	if err := unpack(archive, b.Name, staged); err != nil {
		return fmt.Errorf("embed: fetch %s: %w", b.Name, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("embed: fetch %s: %w", b.Name, err)
	}
	if err := os.Rename(staged, dir); err != nil {
		return fmt.Errorf("embed: fetch %s: %w", b.Name, err)
	}
	return nil
}

func download(ctx context.Context, client *http.Client, b Bundle, to string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.URL, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", b.URL, resp.Status)
	}

	f, err := os.Create(to) //nolint:gosec // a path this package made, beside the model's
	if err != nil {
		return err
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, sum), io.LimitReader(resp.Body, b.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		return err
	case n != b.Size:
		return fmt.Errorf("%d bytes, not the %d pinned", n, b.Size)
	case hex.EncodeToString(sum.Sum(nil)) != b.SHA256:
		return errors.New("not the bytes pinned")
	}
	return nil
}

// unpack writes the archive's files into dir. The digest already vouches for
// the archive, so the checks here are against a pinned digest that was wrong
// rather than against an attacker: regular files, under the bundle's own
// folder, with names the bundle has.
func unpack(archive, name, dir string) error {
	f, err := os.Open(archive) //nolint:gosec // a path this package made, beside the model's
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		folder, file := path.Split(h.Name)
		if h.Typeflag != tar.TypeReg || path.Clean(folder) != name || !bundleFiles[file] {
			return fmt.Errorf("an archive holding %q", h.Name)
		}
		out, err := os.OpenFile(filepath.Join(dir, file), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // a name from bundleFiles
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, maxBundleFile))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	for file := range bundleFiles {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			return fmt.Errorf("an archive without %s", file)
		}
	}
	return nil
}
