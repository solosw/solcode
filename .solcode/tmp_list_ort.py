import os
p = r"C:\Users\solosw\go\pkg\mod\github.com\yalue"
print("exists", os.path.isdir(p))
if os.path.isdir(p):
    for name in os.listdir(p):
        print(name)
