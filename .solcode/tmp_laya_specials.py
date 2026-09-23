import json
from pathlib import Path
p = Path(r"C:\Users\solosw\.solcode\models\laya-onnx\tokenizer.json")
t = json.loads(p.read_text(encoding="utf-8"))
# find special token ids
wanted = {"[CLS]", "[SEP]", "[PAD]", "[UNK]", "[MASK]"}
for at in t.get("added_tokens") or []:
    c = at.get("content")
    if c in wanted:
        print("added", c, at.get("id"), "special", at.get("special"), "lstrip", at.get("lstrip"))
vocab = t["model"]["vocab"]
for c in sorted(wanted):
    print("vocab", c, vocab.get(c))
