import pathlib
lines = pathlib.Path("internal/app/app.go").read_text(encoding="utf-8").splitlines()
for i, l in enumerate(lines, 1):
    if "engineConfig" in l or "OnAskUser" in l or "onAskUser" in l or "AgentRole" in l or "func (a *App) Run" in l or "Coordinator" in l and "New" in l:
        print(f"{i}:{l}")
