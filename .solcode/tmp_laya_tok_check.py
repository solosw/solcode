import json
from pathlib import Path
p = Path(r"C:\Users\solosw\.solcode\models\laya-onnx\tokenizer.json")
t = json.loads(p.read_text(encoding="utf-8"))
print("model.type", t["model"].get("type"))
print("normalizer", json.dumps(t.get("normalizer"), ensure_ascii=False)[:300])
print("pre_tokenizer", json.dumps(t.get("pre_tokenizer"), ensure_ascii=False)[:400])
print("post_processor", json.dumps(t.get("post_processor"), ensure_ascii=False)[:400])
print("decoder", json.dumps(t.get("decoder"), ensure_ascii=False)[:300])
print("added_tokens", len(t.get("added_tokens") or []))
for at in (t.get("added_tokens") or [])[:12]:
    print(" ", at.get("id"), at.get("content"), "special=", at.get("special"))
# special ids from config
print("vocab size", len(t["model"].get("vocab") or {}))
print("merges", len(t["model"].get("merges") or []))
print("dropout", t["model"].get("dropout"))
print("byte_fallback", t["model"].get("byte_fallback"))
print("fuse_unk", t["model"].get("fuse_unk"))
print("ignore_merges", t["model"].get("ignore_merges"))
