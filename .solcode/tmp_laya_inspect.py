import json
import os
from pathlib import Path

root = Path(r"C:\Users\solosw\.solcode\models\laya-onnx")
print("exists", root.is_dir())
if not root.is_dir():
    raise SystemExit(1)

def walk(p: Path, prefix=""):
    for child in sorted(p.iterdir(), key=lambda x: (not x.is_dir(), x.name.lower())):
        if child.is_dir():
            print(f"{prefix}{child.name}/")
            if child.name.lower() not in {".git"}:
                walk(child, prefix + "  ")
        else:
            size = child.stat().st_size
            print(f"{prefix}{child.name}  ({size} bytes)")

walk(root)

interesting = [
    "README.md",
    "open_jev_config.json",
    "config.json",
    "configuration.json",
    "tokenizer_config.json",
    "added_tokens.json",
    "special_tokens_map.json",
]
print("\n=== key files ===")
for name in interesting:
    path = root / name
    if not path.exists():
        print(f"MISSING {name}")
        continue
    text = path.read_text(encoding="utf-8", errors="replace")
    print(f"\n--- {name} ---")
    print(text[:2500])
