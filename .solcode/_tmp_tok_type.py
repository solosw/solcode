import json
from pathlib import Path

p = Path(r"C:/Users/solosw/.solcode/embeddings/tokenizer.json")
d = json.loads(p.read_text(encoding="utf-8"))
print("model.type", (d.get("model") or {}).get("type"))
print("keys", list(d.keys()))
print("truncation", d.get("truncation"))
print("padding", d.get("padding"))
print("bos_token" in str(d.get("added_tokens", [])[:5]))
for t in d.get("added_tokens", [])[:8]:
    print("added", t.get("id"), t.get("content"), t.get("special"))
