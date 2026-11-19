// Package faces finds the faces in photographs and groups them into people
// (#519), on this machine and off the decode a photograph's thumbnails are
// made from: YuNet finds a face and its landmarks, the face is turned onto
// SFace's template, and SFace gives the 128 numbers two faces are compared
// by. What is kept is those numbers, the box, and a small picture of the face
// for the pages that show people.
//
// Grouping is a round of its own rather than a step of each photograph's
// reading, run when new faces have been found: it reads every face once, and
// a library is read a photograph at a time.
package faces

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"log/slog"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
	"github.com/stratus-private-cloud/stratus-backend/internal/files"
	"github.com/stratus-private-cloud/stratus-backend/internal/media"
	"github.com/stratus-private-cloud/stratus-backend/internal/media/thumb"
	"github.com/stratus-private-cloud/stratus-backend/internal/storage"
)

// Batch is how many photographs one call of the pass reads, as the other
// passes do.
const Batch = 8

// PictureSize is the side of the picture faces are read from. YuNet sees it
// fitted in its 640 square, so a face has to be a twentieth of the picture
// to be found at all; what this buys is the pixels the face is aligned from,
// since a face found at 30 pixels of 640 is 75 here.
const PictureSize = 1600

// MinFace is the least side of a face's box, in that picture, worth keeping:
// below it SFace is reading a smudge, and a group of smudges is nobody.
const MinFace = 40

// CropSize is the side of the picture of a face the pages show.
const CropSize = 160

// Dimensions is the size of SFace's vectors.
const Dimensions = 128

// Pipeline is what a face's model name carries besides the models: how they
// were fed. A change to that is a different set of vectors.
const Pipeline = "+1"

// Models is YuNet and SFace: embed.FaceModels.
type Models interface {
	Detect(ctx context.Context, bgr []float32) ([][]float32, error)
	Recognize(ctx context.Context, rgb []float32) ([]float32, error)
}

// Store is the rows, and a transaction to write one photograph's in.
type Store interface {
	db.Faces
	Tx(ctx context.Context, fn func(db.Repo) error) error
}

// Blobs is where the pictures of faces go.
type Blobs interface {
	Get(ctx context.Context, key string, rng storage.Range) (io.ReadCloser, storage.ObjectInfo, error)
	Put(ctx context.Context, key string, r io.Reader, size int64) (storage.ObjectInfo, error)
	Delete(ctx context.Context, key string) error
}

// Pictures decodes a photograph for the pass: thumb.Service.Look.
type Pictures interface {
	Look(ctx context.Context, f db.File, size int, fn func(image.Image) error) error
}

// Service is the pass, the grouping and the forgetting.
type Service struct {
	store    Store
	blobs    Blobs
	pictures Pictures
	models   Models
	model    string

	// found is how many faces have been found since the last grouping.
	mu    sync.Mutex
	found int
}

// New wires the service for the models named model.
func New(store Store, blobs Blobs, pictures Pictures, models Models, model string) *Service {
	return &Service{store: store, blobs: blobs, pictures: pictures, models: models, model: model + Pipeline}
}

// Model is the name every scan this writes records.
func (s *Service) Model() string { return s.model }

// CropKey is where the picture of a face is kept: beside the photograph's
// thumbnails, so the sweep takes it with them.
func CropKey(blobKey string, face int64) string {
	return files.DerivedKey(blobKey, "face-"+strconv.FormatInt(face, 10)+".jpg")
}

// Size implements thumb.Reader.
func (s *Service) Size() int { return PictureSize }

// Wants implements thumb.Reader: whether f is one Batch would read.
func (s *Service) Wants(ctx context.Context, f db.File) (bool, error) {
	return s.store.WantsFaceScan(ctx, f.ID, s.model, time.Now().Add(-files.CacheRetention))
}

// Batch reads up to Batch photographs no scan of this model has reached, as
// semantic.Batch does, and answers how many it reached a verdict about.
func (s *Service) Batch(ctx context.Context, now time.Time) (int, error) {
	pending, err := s.store.PendingFaceScans(ctx, s.model, now.Add(-files.CacheRetention), Batch)
	if err != nil {
		return 0, fmt.Errorf("find photographs to look at: %w", err)
	}
	done := 0
	for _, f := range pending {
		err := s.pictures.Look(ctx, f, PictureSize, func(img image.Image) error { return s.Read(ctx, f, img) })
		if errors.Is(err, thumb.ErrNoThumbnail) {
			err = s.scanned(ctx, f, db.EmbeddingRefused, now)
		}
		switch {
		case errors.Is(err, media.ErrBusy), errors.Is(err, embed.ErrUnavailable),
			errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return done, err
		case errors.Is(err, db.ErrNotFound):
			// Deleted while it was being read.
		case err != nil:
			slog.InfoContext(ctx, "looking for faces", "path", f.Path, "err", err)
			continue
		}
		done++
	}
	return done, nil
}

// found is one face Read keeps.
type found struct {
	row  db.Face
	crop []byte
}

// Read implements thumb.Reader: the faces of one photograph, found, read and
// written down in place of any it had, with their pictures.
func (s *Service) Read(ctx context.Context, f db.File, picture image.Image) error {
	now := time.Now()
	bgr, scale := detectInput(picture)
	outs, err := s.models.Detect(ctx, bgr)
	if err != nil {
		if errors.Is(err, embed.ErrUnavailable) || ctx.Err() != nil {
			return err
		}
		return s.scanned(ctx, f, db.EmbeddingRefused, now)
	}

	b := picture.Bounds()
	var kept []found
	for _, d := range decode(outs, scale) {
		if min(d.W, d.H) < MinFace {
			continue
		}
		v, err := s.models.Recognize(ctx, alignInput(picture, d.Landmarks))
		if err != nil {
			if errors.Is(err, embed.ErrUnavailable) || ctx.Err() != nil {
				return err
			}
			continue
		}
		if len(v) != Dimensions {
			return fmt.Errorf("%w: %d dimensions", embed.ErrDimension, len(v))
		}
		normalize(v)
		crop, err := cropOf(picture, d)
		if err != nil {
			return err
		}
		kept = append(kept, found{crop: crop, row: db.Face{
			FileID: f.ID, BlobKey: f.BlobKey, Score: float32(d.Score), Vector: embed.Quantize(v),
			X: float32(d.X / float64(b.Dx())), Y: float32(d.Y / float64(b.Dy())),
			W: float32(d.W / float64(b.Dx())), H: float32(d.H / float64(b.Dy())),
		}})
	}
	return s.write(ctx, f, kept, now)
}

// write replaces what was recorded of f with kept, and the pictures with it.
// The rows go first and the pictures after, so a picture is never in the
// store under an id no row has.
func (s *Service) write(ctx context.Context, f db.File, kept []found, now time.Time) error {
	var gone []db.Face
	err := s.store.Tx(ctx, func(r db.Repo) error {
		var err error
		if gone, err = r.FacesOf(ctx, f.ID); err != nil {
			return err
		}
		if err = r.DeleteFacesOf(ctx, f.ID); err != nil {
			return err
		}
		err = r.PutFaceScan(ctx, db.FaceScan{FileID: f.ID, State: db.EmbeddingMade, Model: s.model, BlobKey: f.BlobKey, At: now})
		if err != nil {
			return err
		}
		for i := range kept {
			if kept[i].row.ID, err = r.AddFace(ctx, kept[i].row); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, g := range gone {
		if err := s.blobs.Delete(ctx, CropKey(g.BlobKey, g.ID)); err != nil {
			slog.InfoContext(ctx, "deleting the picture of a face", "face", g.ID, "err", err)
		}
	}
	for _, k := range kept {
		if _, err := s.blobs.Put(ctx, CropKey(f.BlobKey, k.row.ID), bytes.NewReader(k.crop), int64(len(k.crop))); err != nil {
			return fmt.Errorf("%w: store the picture of face %d: %w", media.ErrUnreachable, k.row.ID, err)
		}
	}
	s.mu.Lock()
	s.found += len(kept)
	s.mu.Unlock()
	return nil
}

func (s *Service) scanned(ctx context.Context, f db.File, state db.EmbeddingState, now time.Time) error {
	return s.store.PutFaceScan(ctx, db.FaceScan{FileID: f.ID, State: state, Model: s.model, BlobKey: f.BlobKey, At: now})
}

// cropOf is the picture of a face the pages show: a square around its box
// with some of the head and the hair, so it looks like a portrait rather
// than a passport.
func cropOf(picture image.Image, d Detection) ([]byte, error) {
	side := 1.6 * max(d.W, d.H)
	cx, cy := d.X+d.W/2, d.Y+d.H/2
	b := picture.Bounds()
	r := image.Rect(int(math.Round(cx-side/2)), int(math.Round(cy-side/2)),
		int(math.Round(cx+side/2)), int(math.Round(cy+side/2))).Add(b.Min).Intersect(b)
	dst := image.NewRGBA(image.Rect(0, 0, CropSize, CropSize))
	if !r.Empty() {
		// Squeezed rather than padded when the box runs off the picture: a
		// face at the edge is a little wider on the page, not half black.
		thumb.Scale(dst, picture, r)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// GroupEvery is how many new faces are worth a round of grouping before the
// pass has finished: a library read for the first time shows people as it
// goes rather than at the end.
const GroupEvery = 256

// Grouping reports whether a round is due: enough found, or anything found
// and finished is true because the pass has nothing left for now.
func (s *Service) Grouping(finished bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.found >= GroupEvery || finished && s.found > 0
}

// Group puts the faces nobody has under somebody: those like somebody's
// average face under them, and groups of at least MinGroup like each other
// under somebody new. Then the unnamed with no faces left are forgotten.
func (s *Service) Group(ctx context.Context) error {
	s.mu.Lock()
	s.found = 0
	s.mu.Unlock()

	excluded := map[int64][]int64{}
	err := s.store.EachFaceExclusion(ctx, func(face, person int64) error {
		excluded[face] = append(excluded[face], person)
		return nil
	})
	if err != nil {
		return fmt.Errorf("read the faces taken out of people: %w", err)
	}
	var all []face
	err = s.store.EachFace(ctx, s.model, func(f db.Face) error {
		v, derr := embed.Dequantize(f.Vector, Dimensions)
		if derr != nil {
			slog.WarnContext(ctx, "a stored face does not fit", "face", f.ID, "err", derr)
			return nil
		}
		normalize(v)
		all = append(all, face{id: f.ID, person: f.Person, v: v, not: excluded[f.ID]})
		return nil
	})
	if err != nil {
		return fmt.Errorf("read the faces: %w", err)
	}
	g := group(all)
	for person, ids := range g.joins {
		if err := s.store.AssignFaces(ctx, person, ids); err != nil {
			// Merged away meanwhile; the next round finds them loose again.
			slog.InfoContext(ctx, "putting faces under somebody", "person", person, "err", err)
		}
	}
	for _, ids := range g.seeds {
		err := s.store.Tx(ctx, func(r db.Repo) error {
			person, err := r.NewPerson(ctx, "")
			if err != nil {
				return err
			}
			return r.AssignFaces(ctx, person, ids)
		})
		if err != nil {
			return fmt.Errorf("start somebody new: %w", err)
		}
	}
	if _, err := s.store.DeleteEmptyPeople(ctx); err != nil {
		return err
	}
	if len(g.seeds) > 0 || len(g.joins) > 0 {
		slog.InfoContext(ctx, "grouped faces", "joined", len(g.joins), "new_people", len(g.seeds))
	}
	return nil
}

// Forget deletes every face, scan and person and the pictures of the faces:
// what turning faces off does (#519), since what they are is somebody's
// biometric data and off has to mean none of it is kept.
func Forget(ctx context.Context, store db.Faces, blobs Blobs) error {
	var crops []string
	err := store.EachFaceCrop(ctx, func(id int64, blobKey string) error {
		crops = append(crops, CropKey(blobKey, id))
		return nil
	})
	if err != nil {
		return err
	}
	for _, key := range crops {
		if err := blobs.Delete(ctx, key); err != nil {
			return fmt.Errorf("delete the picture of a face: %w", err)
		}
	}
	if err := store.ForgetFaces(ctx); err != nil {
		return err
	}
	if len(crops) > 0 {
		slog.InfoContext(ctx, "forgot every face", "faces", len(crops))
	}
	return nil
}

// Counts is how far the pass has got, for /status.
func (s *Service) Counts(ctx context.Context) (db.FaceCounts, error) {
	return s.store.FaceCounts(ctx, s.model)
}
