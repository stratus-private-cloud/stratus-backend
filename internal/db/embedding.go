package db

import (
	"context"
	"time"
)

// Embeddings is where a photograph's vector is kept, for searching by what is
// in it (#505). The bytes are the caller's: internal/embed decides how a vector
// is written down and this stores them as they come.
//
// A table of its own rather than columns on media, which is where the thumbnail
// state went: a vector is 772 bytes on a row the indexer and the gallery walk
// by the thousand for other reasons, and a foreign key on media's file_id
// takes it away with the metadata in the same statement.
type Embeddings interface {
	// PendingEmbeddings returns the photographs a pass should read next, newest
	// first by the gallery's own column, as PendingThumbs does. One comes back
	// when nothing has read it, when model is not the one that did, when the
	// row no longer points at the bytes it was read from, or when it was
	// refused before staleBefore.
	PendingEmbeddings(ctx context.Context, model string, staleBefore time.Time, limit int) ([]File, error)

	// WantsEmbedding is PendingEmbeddings asked about one file, for a pass
	// that has a photograph's pixels in hand for another reason and would read
	// its vector off them rather than fetch the original again (#519).
	WantsEmbedding(ctx context.Context, fileID int64, model string, staleBefore time.Time) (bool, error)

	// PutEmbedding records one photograph's vector or its refusal, replacing
	// what was there, and answers ErrNotFound for a file with no media row --
	// deleted while it was being read, or never indexed.
	PutEmbedding(ctx context.Context, e Embedding) error

	// EachEmbedding hands fn every vector model made of bytes a file still
	// points at, which is what an index in memory is built from. The slice is
	// fn's to keep.
	EachEmbedding(ctx context.Context, model string, fn func(fileID int64, vector []byte) error) error

	// EmbeddingCounts says how much of the library's photographs model has
	// read, for /status.
	EmbeddingCounts(ctx context.Context, model string) (EmbeddingCounts, error)
}

// EmbeddingState is what became of reading one photograph.
type EmbeddingState int

// What a photograph's reading can be. There is no untried: that is the
// absence of a row.
const (
	EmbeddingMade    EmbeddingState = 1
	EmbeddingRefused EmbeddingState = 2
)

// Embedding is one photograph's answer.
type Embedding struct {
	FileID int64
	State  EmbeddingState
	// Model names the vector space. Two models' vectors cannot be compared, so
	// a vector from another one is as good as none.
	Model string
	// BlobKey is the bytes it was read from, for the reason ThumbVerdict
	// carries one: an overwrite keeps the row and the id.
	BlobKey string
	// Vector is nil for a refusal.
	Vector []byte
	At     time.Time
}

// EmbeddingCounts is how many photographs there are, how many have a vector
// from the model running now, and how many it refused.
type EmbeddingCounts struct {
	Files   int64
	Made    int64
	Refused int64
}

// Waiting is what no pass has reached yet.
func (c EmbeddingCounts) Waiting() int64 { return c.Files - c.Made - c.Refused }
