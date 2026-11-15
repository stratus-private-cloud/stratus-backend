package embed_test

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/stratus-private-cloud/stratus-backend/internal/embed"
)

// TestTokenizerMatchesTheReference compares Encode with what Hugging Face's own
// tokenizer gave for the same text, which is the only definition of right
// there is. It needs the bundle's tokenizer.bin, which is too large to keep in
// the repository, so it runs where STRATUS_TEST_TOKENIZER names one.
func TestTokenizerMatchesTheReference(t *testing.T) {
	path := os.Getenv("STRATUS_TEST_TOKENIZER")
	if path == "" {
		t.Skip("STRATUS_TEST_TOKENIZER is not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	tok, err := embed.ReadTokenizer(f)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile("testdata/siglip2_tokens.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Text string  `json:"text"`
		IDs  []int64 `json:"ids"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if got := tok.Encode(c.Text); !slices.Equal(got, c.IDs) {
			t.Errorf("Encode(%q)\n got  %v\n want %v", c.Text, got, c.IDs)
		}
	}
}

// tinyTokenizer is a vocabulary of ten tokens in the bundle's format, enough
// for every rule Encode has: merges by rank, byte fallback, a special token
// matched literally, truncation and padding.
func tinyTokenizer(t *testing.T) *embed.Tokenizer {
	t.Helper()
	tokens := []string{"<pad>", "<eos>", "<unk>", "a", "b", "ab", "▁", "▁ab", "<0xC3>", "<0xB1>"}
	var buf bytes.Buffer
	buf.WriteString("STK1")
	le := func(v any) {
		if err := binary.Write(&buf, binary.LittleEndian, v); err != nil {
			t.Fatal(err)
		}
	}
	le(uint32(len(tokens)))
	for _, s := range tokens {
		le(uint16(len(s)))
		buf.WriteString(s)
	}
	le(uint32(2))
	le([]uint32{3, 4, 6, 5}) // a+b, then ▁+ab
	le(uint32(1))
	le([]uint32{1})
	tok, err := embed.ReadTokenizer(&buf)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestTheTokenizerFollowsItsRules(t *testing.T) {
	tok := tinyTokenizer(t)
	got := tok.Encode("AB ab ñ<eos>")
	want := []int64{5, 7, 6, 8, 9, 1, 1}
	if !slices.Equal(got[:len(want)], want) || got[len(want)] != 0 || len(got) != embed.ContextLength {
		t.Errorf("Encode = %v, want %v then padding", got[:len(want)+1], want)
	}

	long := tok.Encode(strings.Repeat("ab ", 100))
	if len(long) != embed.ContextLength || long[embed.ContextLength-1] != 1 {
		t.Errorf("a long query is not cut to leave its <eos>: %v", long[embed.ContextLength-3:])
	}

	if _, err := embed.ReadTokenizer(strings.NewReader("JSON")); err == nil {
		t.Error("something that is not a tokenizer was read as one")
	}
}
