import pathlib

def dump_range(path, start, end):
    lines = pathlib.Path(path).read_text(encoding="utf-8").splitlines()
    print(f"\n===== {path}:{start}-{end} =====")
    for i in range(start, min(end, len(lines)) + 1):
        print(f"{i}:{lines[i-1]}")

# Find engineConfig function start
lines = pathlib.Path("internal/app/app.go").read_text(encoding="utf-8").splitlines()
for i, l in enumerate(lines, 1):
    if "func engineConfig" in l:
        print("engineConfig at", i)
        dump_range("internal/app/app.go", i, i + 80)
    if "Coordinator" in l and ("New" in l or "Runner" in l or "Run(" in l):
        print(f"{i}:{l}")
    if "func (a *App)" in l and ("Run" in l or "agent" in l.lower() or "Spawn" in l):
        print(f"{i}:{l}")

# Find permission timeout in main
lines = pathlib.Path("cmd/solcode/main.go").read_text(encoding="utf-8").splitlines()
for i, l in enumerate(lines, 1):
    if "time.After" in l or "AskFunc" in l or "requestPermission" in l or "Permission" in l and "timeout" in l.lower():
        print(f"main:{i}:{l}")

# acp askUser
lines = pathlib.Path("internal/acp/server.go").read_text(encoding="utf-8").splitlines()
for i, l in enumerate(lines, 1):
    if "func (s *Server) askUser" in l or "AskUser" in l:
        print(f"acp:{i}:{l}")
