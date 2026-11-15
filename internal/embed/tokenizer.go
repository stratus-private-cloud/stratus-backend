// Package embed turns a photograph and a sentence into vectors that can be
// compared (#505): SigLIP2's two towers, run by the stratus-embed helper, with
// everything around them -- tokenising, resizing -- done here so the helper
// decides nothing.
package embed

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Tokenizer is Gemma's BPE as SigLIP2 ships it in tokenizer.json, read from
// the compact form the model bundle carries instead: the JSON is 33 MB and
// parsing it costs more memory than the text tower it feeds.
//
// What it reproduces, and all it has to: spaces become "▁", there is no
// pre-tokenisation, merges apply by rank, a character missing from the vocabulary
// falls back to its UTF-8 bytes, and the special tokens match literally.
type Tokenizer struct {
	vocab   map[string]int32
	merges  map[uint64]merge
	special []string // longest first, so "<eos>" never loses to a prefix of it
	bytes   [256]int32
	pad     int32
	eos     int32
	unk     int32
}

type merge struct{ rank, id int32 }

// ContextLength is how many tokens the text tower reads, padding included.
const ContextLength = 64

// maxRunes bounds the work on a query: only ContextLength tokens survive, and
// a token is rarely longer than a word.
const maxRunes = 1024

const tokenizerMagic = "STK1"

// ReadTokenizer reads the bundle's tokenizer.bin: the magic, the vocabulary in
// id order as u16-prefixed strings, the merges in rank order as pairs of ids,
// and the ids of the tokens matched literally.
func ReadTokenizer(r io.Reader) (*Tokenizer, error) {
	br := bufio.NewReader(r)
	magic := make([]byte, 4)
	if _, err := io.ReadFull(br, magic); err != nil || string(magic) != tokenizerMagic {
		return nil, errors.New("embed: not a tokenizer")
	}
	var n uint32
	if err := binary.Read(br, binary.LittleEndian, &n); err != nil {
		return nil, fmt.Errorf("embed: tokenizer: %w", err)
	}
	t := &Tokenizer{vocab: make(map[string]int32, n), pad: -1, eos: -1, unk: -1}
	for i := range t.bytes {
		t.bytes[i] = -1
	}
	tokens := make([]string, n)
	for i := range tokens {
		var l uint16
		if err := binary.Read(br, binary.LittleEndian, &l); err != nil {
			return nil, fmt.Errorf("embed: tokenizer: %w", err)
		}
		b := make([]byte, l)
		if _, err := io.ReadFull(br, b); err != nil {
			return nil, fmt.Errorf("embed: tokenizer: %w", err)
		}
		tokens[i] = string(b)
		t.vocab[tokens[i]] = int32(i)
	}
	if err := binary.Read(br, binary.LittleEndian, &n); err != nil {
		return nil, fmt.Errorf("embed: tokenizer: %w", err)
	}
	t.merges = make(map[uint64]merge, n)
	pair := make([]uint32, 2)
	for rank := range int32(n) {
		if err := binary.Read(br, binary.LittleEndian, pair); err != nil {
			return nil, fmt.Errorf("embed: tokenizer: %w", err)
		}
		if int(pair[0]) >= len(tokens) || int(pair[1]) >= len(tokens) {
			return nil, errors.New("embed: tokenizer: merge out of range")
		}
		id, ok := t.vocab[tokens[pair[0]]+tokens[pair[1]]]
		if !ok {
			return nil, errors.New("embed: tokenizer: merge makes no token")
		}
		key := uint64(pair[0])<<32 | uint64(pair[1])
		if _, dup := t.merges[key]; !dup {
			t.merges[key] = merge{rank, id}
		}
	}
	if err := binary.Read(br, binary.LittleEndian, &n); err != nil {
		return nil, fmt.Errorf("embed: tokenizer: %w", err)
	}
	ids := make([]uint32, n)
	if err := binary.Read(br, binary.LittleEndian, ids); err != nil {
		return nil, fmt.Errorf("embed: tokenizer: %w", err)
	}
	for _, id := range ids {
		if int(id) >= len(tokens) {
			return nil, errors.New("embed: tokenizer: special token out of range")
		}
		t.special = append(t.special, tokens[id])
	}
	sortLongestFirst(t.special)

	for i := range 256 {
		if id, ok := t.vocab[fmt.Sprintf("<0x%02X>", i)]; ok {
			t.bytes[i] = id
		}
	}
	t.pad, t.eos, t.unk = t.vocab["<pad>"], t.vocab["<eos>"], t.vocab["<unk>"]
	if _, ok := t.vocab["<eos>"]; !ok {
		return nil, errors.New("embed: tokenizer: no <eos>")
	}
	return t, nil
}

func sortLongestFirst(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && len(s[j]) > len(s[j-1]); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// Encode is the text tower's input for one query: lowercased, as SigLIP2 was
// trained, truncated to leave room for <eos>, and padded to ContextLength.
func (t *Tokenizer) Encode(text string) []int64 {
	text = strings.ToLower(text)
	if utf8.RuneCountInString(text) > maxRunes {
		text = string([]rune(text)[:maxRunes])
	}
	text = strings.ReplaceAll(text, " ", "▁")

	var ids []int32
	for text != "" && len(ids) < ContextLength {
		at, sp := len(text), ""
		for _, s := range t.special {
			if i := strings.Index(text, s); i >= 0 && (i < at || i == at && len(s) > len(sp)) {
				at, sp = i, s
			}
		}
		ids = append(ids, t.bpe(text[:at])...)
		if sp != "" {
			ids = append(ids, t.vocab[sp])
		}
		text = text[at+len(sp):]
	}

	out := make([]int64, ContextLength)
	if len(ids) > ContextLength-1 {
		ids = ids[:ContextLength-1]
	}
	for i, id := range ids {
		out[i] = int64(id)
	}
	out[len(ids)] = int64(t.eos)
	for i := len(ids) + 1; i < ContextLength; i++ {
		out[i] = int64(t.pad)
	}
	return out
}

// bpe merges one stretch of text with no special token in it. Quadratic, which
// is nothing at the length of a query.
func (t *Tokenizer) bpe(s string) []int32 {
	if s == "" {
		return nil
	}
	syms := make([]int32, 0, len(s))
	var buf [utf8.UTFMax]byte
	for _, r := range s {
		if id, ok := t.vocab[string(r)]; ok {
			syms = append(syms, id)
			continue
		}
		for _, b := range buf[:utf8.EncodeRune(buf[:], r)] {
			if id := t.bytes[b]; id >= 0 {
				syms = append(syms, id)
			} else {
				syms = append(syms, t.unk)
			}
		}
	}
	for len(syms) > 1 {
		best, at := merge{rank: -1}, -1
		for i := 0; i+1 < len(syms); i++ {
			m, ok := t.merges[uint64(uint32(syms[i]))<<32|uint64(uint32(syms[i+1]))] //nolint:gosec // ids below the vocabulary's size
			if ok && (at < 0 || m.rank < best.rank) {
				best, at = m, i
			}
		}
		if at < 0 {
			break
		}
		syms[at] = best.id
		syms = append(syms[:at+1], syms[at+2:]...)
	}
	return syms
}
