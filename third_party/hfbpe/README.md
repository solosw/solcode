# hfbpe

Load a Hugging Face `tokenizer.json` in Go and get the same token ids the
Python library gives you, **for byte-level BPE only**.

Go services that call a model need the model's tokenizer. Today that usually
means shelling out to Python, running a sidecar, or reimplementing the
tokenizer and hoping. The last option is the dangerous one: a tokenizer that
disagrees with the one a model was trained against doesn't crash, it quietly
degrades output, and the drift is invisible until someone compares token
counts by hand.

So this package does one algorithm and **refuses to load anything else**.

```go
tok, err := hfbpe.LoadFile("tokenizer.json")
if err != nil {
    log.Fatal(err)   // hfbpe: unsupported model.type "WordPiece"
}

enc := tok.Encode("hello world")
fmt.Println(enc.IDs, enc.Tokens)
fmt.Println(tok.Decode(enc.IDs, true))
```

## Scope

**Supported**

- `model.type` `"BPE"`, with merges in either the modern `[["a","b"], …]`
  form or the legacy `["a b", …]` form
- `ByteLevel` pre-tokenizer, post-processor and decoder, the GPT-2 and
  RoBERTa family
- added and special tokens, matched literally, longest first
- `model.ignore_merges`

**Not supported, and rejected by name, never ignored**

| out of scope | why |
|---|---|
| **WordPiece** | different algorithm; needs its own greedy longest-match and `##` prefix handling |
| **Unigram** | different algorithm; needs a Viterbi decode over log probabilities |
| all normalizers (NFC, NFD, Lowercase, Replace, Sequence, …) | each changes the input before tokenization |
| any pre-tokenizer other than `ByteLevel` | the merge table was trained against a specific split |
| `dropout`, `byte_fallback`, `fuse_unk` | change the merge algorithm |
| `continuing_subword_prefix`, `end_of_word_suffix` | change the merge algorithm |
| token offsets, truncation, padding | not implemented |

Each of these produces an `*UnsupportedError` naming the component:

```go
var ue *hfbpe.UnsupportedError
if errors.As(err, &ue) {
    log.Printf("cannot handle %s = %q", ue.Component, ue.Value)
}
```

That refusal is the point of the package. Loading a WordPiece file and
tokenizing it as BPE would produce plausible-looking garbage.

## Verification

The suite checks token ids, token strings and decoded text against Hugging
Face `tokenizers` 0.22.2 across **204 cases and 1241 reference tokens**, all
matching exactly. The corpus is built to break things:

- whitespace runs, tabs, newlines, `\r\n`, leading and trailing spaces
- contractions, including `'S` and `y'all'd've`, which the pattern
  deliberately does *not* fold
- digits, mixed alphanumeric runs, punctuation runs, URLs
- Greek, Cyrillic, Hebrew, Arabic, CJK and Hangul
- emoji, ZWJ family sequences, regional-indicator flags
- no-break and ideographic spaces, zero-width space, combining marks
- special tokens embedded mid-string, adjacent, and truncated

Plus tests that every out-of-scope file is rejected with the right component
name, that both merge encodings agree, and that a malformed merge line is an
error rather than a silent mis-parse.

Fixtures are regenerable, so a future `tokenizers` release changing behaviour
surfaces as a test failure rather than silent drift:

```
pip install tokenizers
python cmd/genfixtures/gen.py
go test ./...
```

The bundled tokenizer is trained from scratch on synthetic text by that
script, so the repository carries no third-party vocabulary.

## One implementation note

GPT-2's pre-tokenizer pattern ends with `\s+(?!\S)|\s+`. Go's `regexp` is
RE2, which has no lookahead, so that pattern cannot be used directly. The
split is hand-written instead: a whitespace run followed by a non-space
yields all but its last character, and the final space attaches to the
following piece. This is the behaviour the negative lookahead produces
through backtracking, and getting it wrong shifts every token after the first
double space.

## Performance

```
BenchmarkEncodeShort         4.4 µs/op    11 MB/s
BenchmarkEncodeParagraph     127 µs/op    41 MB/s
BenchmarkLoad                758 µs/op
```

Tokenized words are cached, bounded at 65536 entries so untrusted input
can't grow it without limit.

## Licence

BSD-3-Clause.
