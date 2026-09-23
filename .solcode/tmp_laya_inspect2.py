from pathlib import Path
import json

root = Path(r"C:\Users\solosw\.solcode\models\laya-onnx")
print("=== rl_agent_config.json ===")
print((root / "rl_agent_config.json").read_text(encoding="utf-8"))
print("\n=== README more ===")
text = (root / "README.md").read_text(encoding="utf-8", errors="replace")
print(text[2500:7000])
print("\n=== tokenizer.json peek ===")
tok = json.loads((root / "tokenizer.json").read_text(encoding="utf-8"))
print("keys", list(tok.keys()))
print("model type", (tok.get("model") or {}).get("type"))
added = tok.get("added_tokens") or []
print("added_tokens", len(added))
for t in added[:15]:
    print(t)
# look for marker-like tokens
for t in added:
    c = t.get("content") if isinstance(t, dict) else str(t)
    if any(x in str(c).upper() for x in ("STATE", "OPT", "Q", "MARKER", "CLS", "SEP", "PAD")):
        print("SPECIALISH", t)
print("\nfiles sizes:")
for p in sorted(root.iterdir()):
    if p.is_file():
        print(p.name, p.stat().st_size)
