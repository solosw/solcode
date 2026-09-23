import pathlib

def show(path, starts, ends=None):
    p = pathlib.Path(path)
    lines = p.read_text(encoding="utf-8").splitlines()
    print(f"\n===== {path} =====")
    for i, line in enumerate(lines, 1):
        if any(s in line for s in starts):
            print(f"{i}:{line}")
    if ends:
        for a,b in ends:
            print(f"\n--- {path}:{a}-{b} ---")
            for i in range(a, min(b, len(lines))+1):
                print(f"{i}:{lines[i-1]}")

show("internal/tui/model.go", ["AskUser", "pendingAsk", "resolveAsk", "handleAsk", "time.After"])
show("internal/app/app.go", ["engineConfig", "OnAskUser", "onAskUser", "AgentRole", "func (a *App)"])
show("cmd/solcode/main.go", ["AskUser", "time.After", "Permission", "askUser"])
