import pathlib
import re

def search(root, pattern):
    pats = re.compile(pattern)
    root = pathlib.Path(root)
    for p in root.rglob("*.go"):
        try:
            text = p.read_text(encoding="utf-8", errors="ignore")
        except Exception:
            continue
        for i, line in enumerate(text.splitlines(), 1):
            if pats.search(line):
                print(f"{p}:{i}:{line.strip()}")

print("=== TUI AskUser ===")
search("internal/tui", r"AskUser|pendingAsk|resolveAskUser|handleAskUser|time\.After")
print("=== APP wiring ===")
search("internal/app", r"func engineConfig|OnAskUser|AgentRole|func \(a \*App\)")
print("=== MAIN AskUser/timeout ===")
search("cmd/solcode", r"time\.After|PermissionRequest|AskUser|timeout|askUser")
print("=== ENGINE isMain/isTask AskUser ===")
search("internal/engine", r"isMain|isTask|OnAskUser|AskUser:")
print("=== TESTS AskUser ===")
search(".", r"AskUser|ask_user")
