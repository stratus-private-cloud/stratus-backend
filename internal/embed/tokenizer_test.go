package embed_test

import (
	"encoding/json"
	"os"
	"slices"
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
