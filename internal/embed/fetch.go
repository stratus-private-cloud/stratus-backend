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
	"slices"
	"strings"
)

// Bundle is a published model: what it is called, where it is, the bytes it
// has to be, and the only names its archive may put in the directory -- all of
// which it must.
type Bundle struct {
	Name   string
	URL    string
	SHA256 string
	Size   int64
	Files  []string
}

// SigLIP2 is the model this build reads photographs with, a release asset
// built by build/embed/model, as the demo's media are.
var SigLIP2 = Bundle{
	Name:   "siglip2-base-patch16-224-int8.2",
	URL:    "https://github.com/stratus-private-cloud/stratus-backend/releases/download/model-siglip2-v2/siglip2-base-patch16-224-int8.2.tar.gz",
	SHA256: "b8c07e77995b9a18f03a357dbf8f9f80ceadc630eb6f49147d9bb5cdf50bcf03",
	Size:   347709300,
	Files:  []string{"vision.ort", "text.ort", "tokenizer.bin", "NAME", "LICENSE", "NOTICE"},
}

// Faces is the models this build finds and tells faces apart with (#519):
// OpenCV Zoo's YuNet and SFace, converted by build/embed/model/faces.py and
// published beside SigLIP2.
var Faces = Bundle{
	Name:   "faces-yunet-2023mar-sface-2021dec.1",
	URL:    "https://github.com/stratus-private-cloud/stratus-backend/releases/download/model-faces-v1/faces-yunet-2023mar-sface-2021dec.1.tar.gz",
	SHA256: "bd47ed372694a36330d5ae55758fcbd4b53253ea3245b7b521f8f8343f9cb7bf",
	Size:   36073050,
	Files:  []string{"yunet.ort", "sface.ort", "NAME", "LICENSE", "NOTICE"},
}

// maxBundleFile bounds what one file of an archive may unpack to.
const maxBundleFile = 1 << 30

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
	if err := unpack(archive, b, staged); err != nil {
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
func unpack(archive string, b Bundle, dir string) error {
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
		if h.Typeflag != tar.TypeReg || path.Clean(folder) != b.Name || !slices.Contains(b.Files, file) {
			return fmt.Errorf("an archive holding %q", h.Name)
		}
		out, err := os.OpenFile(filepath.Join(dir, file), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // a name from b.Files
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
	for _, file := range b.Files {
		if _, err := os.Stat(filepath.Join(dir, file)); err != nil {
			return fmt.Errorf("an archive without %s", file)
		}
	}
	return nil
}
