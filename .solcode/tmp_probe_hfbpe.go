package main

import (
	"fmt"
	"os"
	"unicode/utf8"

	"github.com/hashirmuzaffar/hfbpe"
	"golang.org/x/text/unicode/norm"
)

func main() {
	path := `C:\Users\solosw\.solcode\models\laya-onnx\tokenizer.json`
	tok, err := hfbpe.LoadFile(path)
	if err != nil {
		fmt.Println("LoadFile:", err)
		os.Exit(1)
	}
	fmt.Println("vocab", tok.VocabSize())
	for _, name := range []string{"[CLS]", "[SEP]", "[PAD]", "[UNK]", "[MASK]"} {
		id, ok := tok.TokenToID(name)
		fmt.Printf("%s -> %d ok=%v\n", name, id, ok)
	}
	texts := []string{
		"hello world",
		"I was charged twice for the same order.",
		"choice question: Which department should handle this request?",
		"billing: invoices, payments, refunds",
	}
	for _, text := range texts {
		n := norm.NFC.String(text)
		enc := tok.Encode(n)
		fmt.Printf("%q -> %v (runes=%d bytes=%d)\n", text, enc.IDs, utf8.RuneCountInString(text), len(text))
	}
}
