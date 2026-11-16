package thumb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"log/slog"
	"time"

	"github.com/stratus-private-cloud/stratus-backend/internal/db"
	"github.com/stratus-private-cloud/stratus-backend/internal/files"
	"github.com/stratus-private-cloud/stratus-backend/internal/media"
)

// Pictures made before anybody asks for them, which is the indexer's second
// half rather than a loop of its own (#341).
//
// What was rejected, and stays rejected, is generating in the upload path with
// the client waiting on a decode per PUT -- see the top of thumb.go. This is
// the other shape: announced by the write, a query for everything else, a
// verdict per file, and a number somebody can watch. A thumbnail is the same
// file opened for the same reason a second time, so it is the same pass.
//
// Two rules make it safe to run at all, and both are in Prepare. It **loses to
// whoever is looking**: a decoder is taken only when nothing else wants one,
// because a picture made with an idle CPU is worth having and the same picture
// made with the slot a grid is waiting for is the problem this was built to
// fix. And it **writes down what it finds**, so the second pass is not the
// first one again.

// passSizes are what a pass makes: the listing's box and the grid's cell, out
// of one decode. The largest stays lazy -- 1200 is one photograph at a time in
// the viewer, where a wait is a wait for the one picture somebody is looking
// at, and making it for a library would be most of the bytes for the rarest
// ask.
var passSizes = []thumbSize{thumbSmall, thumbMedium}

// Batch is how many files one pass looks at before the loop driving it
// goes back to what else has arrived.
//
// Small, and it is the indexer's number divided by what a file costs here: a
// batch of metadata is a few ranged reads and a batch of pictures is a decode
// each, so eight is about the longest this may hold the loop while somebody's
// upload waits to be indexed.
const Batch = 8

// Refused reports whether the index says this build has already tried to draw
// a file and could not, and that the verdict still stands.
//
// It is the one rule every reader of the column shares -- a listing, a grid
// and the `has-preview` a WebDAV client reads -- so it is here, beside the
// pass that writes the verdict, rather than copied into each of them.
//
// **The week is what makes a page safe to believe it.** It is the same
// files.CacheRetention the pass gives a refusal and the sweep gives the note
// beside the picture: what this build cannot draw today an upgraded one may,
// and a page that stopped offering for ever would be the reason nothing ever
// tried again.
func Refused(state db.MediaState, now time.Time) bool {
	return state.Thumb == db.ThumbRefused && now.Sub(state.ThumbAt) < files.CacheRetention
}

// Reader is something else made of a photograph's pixels, read off the decode
// its thumbnails come from so that the original is fetched once for all of
// them (#519): on S3 every read is a request somebody pays for.
type Reader interface {
	// Wants reports whether f still needs what this reader makes, so that a
	// decode made for the thumbnails is handed only to who has a use for it.
	Wants(ctx context.Context, f db.File) (bool, error)
	// Read makes it from the picture, fitted in a box of PictureSize and
	// turned the right way up. It holds the decoder while it runs.
	Read(ctx context.Context, f db.File, picture image.Image) error
}

// PictureSize is the box a Reader's picture fits in: the grid's, so a reader
// sees what the grid shows, decoded rather than read back from its JPEG.
const PictureSize = int(thumbMedium)

// Also hands every decode a pass makes to r as well. It is wiring, called
// before the passes start.
func (t *Service) Also(r Reader) { t.readers = append(t.readers, r) }

// Hold reads a photograph whole when a pass is about to decode it, so that
// the indexer and the pass read the same bytes and the store is asked once
// (#519). Nil for anything else: a film is read where it lies, over ranges,
// and a photograph past maxThumbSource is read the way it always was rather
// than held in memory.
func (t *Service) Hold(ctx context.Context, f db.File) (io.ReadSeeker, error) {
	if !media.IsImage(f.Path) || !CanThumbnail(f.Path, f.Size) || f.Size > maxThumbSource {
		return nil, nil
	}
	var held []byte
	err := t.fromStore(ctx, f, func(r io.ReadSeeker) error {
		var err error
		held, err = io.ReadAll(r)
		return err
	})
	if err != nil {
		return nil, err
	}
	return bytes.NewReader(held), nil
}

// Prepare makes the pictures a grid and a listing will ask for, before either
// does, and writes down what became of them.
//
// Three ways it does not make one, and only the last is the file's fault:
// media.ErrBusy, which is the pass losing to whoever is looking; an error reaching
// the bytes, which is nobody's verdict and leaves the row untouched so the
// next pass tries again (#157); and a file this build cannot read, which is
// remembered.
//
// It asks the store nothing before it decodes: the row is what says a picture
// is wanted, since the lazy path writes it down as well (#341), and asking
// the store again was a HEAD per size per file (#519).
func (t *Service) Prepare(ctx context.Context, f db.File) (db.ThumbState, error) {
	return t.PrepareFrom(ctx, f, nil)
}

// PrepareFrom is Prepare over bytes Hold has already read, or over the store
// when held is nil.
func (t *Service) PrepareFrom(ctx context.Context, f db.File, held io.ReadSeeker) (db.ThumbState, error) {
	v := verdict{note: verdictKey(f.BlobKey), says: ErrNoThumbnail, file: f.ID, blobKey: f.BlobKey}

	if !CanThumbnail(f.Path, f.Size) {
		// Written down here although the lazy path writes nothing for the same
		// answer, and the difference is that this one has a queue: a pass
		// ordered by date would otherwise offer the same unreadable file at
		// the head of every batch for ever. What makes it safe to record a
		// property of the build rather than of the file is that the verdict
		// goes stale -- see PendingThumbs.
		t.record(ctx, v, db.ThumbRefused)
		return db.ThumbRefused, nil
	}

	release, taken := t.decodeIfIdle()
	if !taken {
		return 0, media.ErrBusy
	}
	defer release()

	readers := t.wanting(ctx, f)
	made := make(map[thumbSize][]byte, len(passSizes))
	err := t.read(ctx, f, held, func(body io.ReadSeeker) error {
		pic, err := t.decode(ctx, f, body, largest(passSizes))
		if err != nil {
			return err
		}
		for _, size := range passSizes {
			if made[size], err = pic.at(size); err != nil {
				return err
			}
		}
		if len(readers) > 0 {
			picture := pic.picture(thumbMedium)
			for _, r := range readers {
				if err := r.Read(ctx, f, picture); err != nil {
					// Its own pass comes round for it, at the price of the
					// read this was saving.
					slog.InfoContext(ctx, "reading a photograph beside its thumbnails", "path", f.Path, "err", err)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.remember(ctx, v, err)
		if isVerdict(ctx, err) {
			return db.ThumbRefused, nil
		}
		return 0, err
	}

	for size, picture := range made {
		if err := t.keep(ctx, f, size, picture); err != nil {
			// The decode happened and the bytes are right; what failed is the
			// store. Saying so and leaving the row alone is what brings the
			// file back on the next pass.
			return 0, err
		}
	}
	t.record(ctx, v, db.ThumbMade)
	return db.ThumbMade, nil
}

// wanting is the readers that have a use for f's pixels. One that cannot say
// is left out: its own pass will ask again.
func (t *Service) wanting(ctx context.Context, f db.File) []Reader {
	var out []Reader
	for _, r := range t.readers {
		switch ok, err := r.Wants(ctx, f); {
		case err != nil:
			slog.InfoContext(ctx, "asking whether a photograph is wanted", "path", f.Path, "err", err)
		case ok:
			out = append(out, r)
		}
	}
	return out
}

// read hands use the held bytes, or the original from the store.
func (t *Service) read(ctx context.Context, f db.File, held io.ReadSeeker, use func(io.ReadSeeker) error) error {
	if held == nil {
		return t.fromStore(ctx, f, use)
	}
	if _, err := held.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return use(held)
}

// PrepareBatch makes the pictures of up to Batch files that have none,
// newest first, and answers how many it reached a verdict about.
//
// Photographs before films, and the batch is shared: a library is mostly
// photographs, the gallery is the grid somebody opens, and a film nobody has
// reached still makes its own picture the moment anybody looks at it. media.ErrBusy
// is not a failure -- it is the pass standing aside, and the caller decides
// when to come back.
func (t *Service) PrepareBatch(ctx context.Context, now time.Time) (int, error) {
	var done int
	for _, kind := range []db.Kind{db.KindImage, db.KindVideo} {
		// The week is files.CacheRetention, which is the same one the sweep
		// takes a note after: the two halves of a refusal expire together
		// because they are given the same number (#338).
		pending, err := t.meta.PendingThumbs(ctx, kind, files.DerivedGeneration,
			now.Add(-files.CacheRetention), Batch-done)
		if err != nil {
			return done, fmt.Errorf("find pictures to make: %w", err)
		}

		for _, f := range pending {
			switch _, err := t.Prepare(ctx, f); {
			case errors.Is(err, media.ErrBusy):
				return done, media.ErrBusy
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				return done, err
			case err != nil:
				// The bytes could not be reached, which says nothing about the
				// file: the row is untouched and the next pass offers it again.
				slog.InfoContext(ctx, "making a thumbnail ahead", "path", f.Path, "err", err)
			}
			done++
		}
		if done >= Batch {
			break
		}
	}
	return done, nil
}

// keep stores one picture a pass made.
func (t *Service) keep(ctx context.Context, f db.File, size thumbSize, picture []byte) error {
	key := thumbKey(f.BlobKey, size)
	if _, err := t.blobs.Put(ctx, key, bytes.NewReader(picture), int64(len(picture))); err != nil {
		return fmt.Errorf("%w: store %q: %w", media.ErrUnreachable, key, err)
	}
	return nil
}

// decodeIfIdle takes a decoder only when nothing else wants one.
//
// That is the whole of how a pass loses to whoever is looking, and it is why
// there is no second semaphore: the counter in front of the decoders already
// says how much of this machine thumbnails hold, so a pass that reads it as
// anything but zero has its answer. It counts itself in while it decodes, so a
// request arriving meanwhile queues as it would behind any other decode rather
// than being told the machine is free.
func (t *Service) decodeIfIdle() (func(), bool) {
	if t.inQueue.Load() != 0 {
		return nil, false
	}
	t.inQueue.Add(1)
	select {
	case t.decoding <- struct{}{}:
		return func() { <-t.decoding; t.inQueue.Add(-1) }, true
	default:
		// Somebody took the last slot between the two lines above, which is
		// the race this answers by standing aside rather than by locking.
		t.inQueue.Add(-1)
		return nil, false
	}
}

// Look decodes f and hands fn its picture as a Reader would get it, for a
// reader's own pass over what no thumbnail pass will decode again -- a library
// whose thumbnails were made before the reader was switched on.
//
// ErrNoThumbnail for a file this build cannot draw, and media.ErrBusy when
// somebody is looking, as Prepare. An error from fn comes back as it is.
func (t *Service) Look(ctx context.Context, f db.File, fn func(image.Image) error) error {
	if !CanThumbnail(f.Path, f.Size) {
		return fmt.Errorf("%w: %q", ErrNoThumbnail, f.Path)
	}
	release, taken := t.decodeIfIdle()
	if !taken {
		return media.ErrBusy
	}
	defer release()

	var picture image.Image
	err := t.fromStore(ctx, f, func(body io.ReadSeeker) error {
		pic, err := t.decode(ctx, f, body, thumbMedium)
		if err != nil {
			return err
		}
		picture = pic.picture(thumbMedium)
		return nil
	})
	if err != nil {
		return err
	}
	return fn(picture)
}
