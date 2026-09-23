import json
import os
import urllib.request
from pathlib import Path

ROOT = Path(r"C:\software\projects\solcode\third_party\hfbpe")
ROOT.mkdir(parents=True, exist_ok=True)

api = "https://api.github.com/repos/hashirmuzaffar/hfbpe/git/trees/main?recursive=1"
req = urllib.request.Request(api, headers={"User-Agent": "solcode"})
with urllib.request.urlopen(req, timeout=60) as r:
    tree = json.load(r)["tree"]

wanted = []
for item in tree:
    if item.get("type") != "blob":
        continue
    path = item["path"]
    if path.startswith("internal/testdata/") or path.endswith("_test.go"):
        continue
    if path.endswith(".go") or path in ("go.mod", "README.md", "LICENSE", "LICENSE.md"):
        wanted.append(path)

print("files", wanted)
for path in wanted:
    url = f"https://cdn.jsdelivr.net/gh/hashirmuzaffar/hfbpe@main/{path}"
    dest = ROOT / path
    dest.parent.mkdir(parents=True, exist_ok=True)
    try:
        with urllib.request.urlopen(url, timeout=60) as r:
            data = r.read()
        dest.write_bytes(data)
        print("ok", path, len(data))
    except Exception as e:
        print("fail", path, e)
        # fallback github api contents
        curl = f"https://api.github.com/repos/hashirmuzaffar/hfbpe/contents/{path}"
        try:
            req2 = urllib.request.Request(curl, headers={"User-Agent": "solcode"})
            with urllib.request.urlopen(req2, timeout=60) as r:
                meta = json.load(r)
            import base64
            data = base64.b64decode(meta["content"])
            dest.write_bytes(data)
            print("ok-api", path, len(data))
        except Exception as e2:
            print("fail2", path, e2)

# rewrite module path for local replace if needed
mod = ROOT / "go.mod"
if mod.exists():
    text = mod.read_text(encoding="utf-8")
    print("go.mod:\n", text)
