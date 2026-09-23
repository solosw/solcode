import pathlib

lines = pathlib.Path("internal/tui/model.go").read_text(encoding="utf-8").splitlines()
for i, l in enumerate(lines, 1):
    if ("func (m Model)" in l and ("Ask" in l or "ask" in l or "Permission" in l)) or "AskUserRequestMsg" in l or "time.After" in l or "resolveAsk" in l or "handleAsk" in l:
        print(f"{i}:{l}")

print("--- app.go ---")
lines = pathlib.Path("internal/app/app.go").read_text(encoding="utf-8").splitlines()
for i, l in enumerate(lines, 1):
    if "engineConfig" in l or "OnAskUser" in l or "onAskUser" in l or "AgentRole" in l:
        print(f"{i}:{l}")

print("--- main.go ---")
lines = pathlib.Path("cmd/solcode/main.go").read_text(encoding="utf-8").splitlines()
for i, l in enumerate(lines, 1):
    if any(x in l for x in ["AskUser", "time.After", "Permission", "askUser"]):
        print(f"{i}:{l}")
