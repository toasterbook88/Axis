package facts

// DiskWeightsDiscoveryScript inventories on-disk weights on a remote node.
// Marker axis-disk-weights-v1 is the fake-executor contains key.
// Python is best-effort: missing interpreter yields an empty scan, not a collect failure.
const DiskWeightsDiscoveryScript = `python3 - <<'PY'
# axis-disk-weights-v1
import json, os, sys
from pathlib import Path

MIN = 20 * 1024 * 1024
MAX_FILES = 400
SKIP = {".git","node_modules","__pycache__",".venv","venv","target",".tox",".cargo",
        ".npm",".rustup","Library","Containers","Applications",".Trash","lost+found",
        ".cache",".ollama","blobs"}
SYS_PREFIX = ("/usr","/bin","/sbin","/lib","/lib64","/boot","/proc","/sys","/dev",
              "/run","/snap","/var/lib/docker","/var/lib/containerd","/var/log",
              "/var/cache","/System","/Library","/Applications","/private")

def is_sys(p):
    p = os.path.normpath(p)
    return any(p == s or p.startswith(s + os.sep) for s in SYS_PREFIX)

weights = []
seen = set()
truncated = False

def add(w):
    path = os.path.normpath(w["path"])
    if path in seen:
        return
    seen.add(path)
    weights.append(w)

def gguf_magic(path):
    try:
        with open(path, "rb") as f:
            return f.read(4) == b"GGUF"
    except OSError:
        return False

home = str(Path.home())
hub = Path(home) / ".cache" / "huggingface" / "hub"
try:
    hub = hub.resolve()
except OSError:
    pass
if hub.is_dir():
    try:
        for e in hub.iterdir():
            if not e.is_dir() or not e.name.startswith("models--"):
                continue
            total = 0
            fmt = "safetensors"
            snap = e / "snapshots"
            if not snap.is_dir():
                continue
            for root, dirs, files in os.walk(snap, topdown=True):
                depth = root[len(str(e)):].count(os.sep)
                if depth > 4:
                    dirs[:] = []
                    continue
                for fn in files:
                    low = fn.lower()
                    fp = os.path.join(root, fn)
                    try:
                        sz = os.path.getsize(fp)
                    except OSError:
                        continue
                    if low.endswith(".gguf"):
                        fmt = "gguf"
                        total += sz
                    elif low.endswith(".safetensors"):
                        total += sz
            if total >= MIN:
                rest = e.name[len("models--"):]
                parts = rest.split("--")
                label = "/".join(parts) if len(parts) >= 2 else rest
                add({"name": label, "path": str(e), "bytes": total, "format": fmt,
                     "source": "hf-hub", "kind": "model"})
    except OSError:
        pass

man = Path(home) / ".ollama" / "models" / "manifests"
if man.is_dir():
    for root, dirs, files in os.walk(man):
        for fn in files:
            p = Path(root) / fn
            rel = str(p.relative_to(man)).replace("\\", "/")
            parts = rel.split("/")
            if len(parts) < 2:
                continue
            name, tag = parts[-2], parts[-1]
            label = tag if name == "library" else name + ":" + tag
            try:
                sz = p.stat().st_size
            except OSError:
                sz = 0
            add({"name": label, "path": str(p), "bytes": sz, "format": "ollama",
                 "source": "ollama-manifest", "kind": "model"})

hits = []
def walk_root(root, source):
    global truncated
    if truncated or not os.path.isdir(root):
        return
    n = 0
    for dirpath, dirs, files in os.walk(root, topdown=True):
        if is_sys(dirpath):
            dirs[:] = []
            continue
        dirs[:] = [d for d in dirs if d not in SKIP]
        for fn in files:
            if n >= MAX_FILES:
                truncated = True
                return
            low = fn.lower()
            if not (low.endswith(".gguf") or low.endswith(".safetensors")):
                continue
            fp = os.path.join(dirpath, fn)
            try:
                sz = os.path.getsize(fp)
            except OSError:
                continue
            if sz < MIN:
                continue
            if low.endswith(".gguf") and not gguf_magic(fp):
                continue
            hits.append((sz, fp, source))
            n += 1

for p in (os.path.join(home, "models"), os.path.join(home, "Storage"), "/opt/models", "/models"):
    walk_root(p, "well-known")

# remaining local mounts (best-effort df)
try:
    import subprocess
    df = subprocess.check_output(["df", "-kPl"], text=True, timeout=3)
    mounts = []
    for line in df.splitlines()[1:]:
        parts = line.split()
        if len(parts) < 6:
            continue
        mnt = " ".join(parts[5:])
        if mnt in ("/boot",) or mnt.startswith("/boot/") or mnt.startswith("/snap/"):
            continue
        mounts.append(mnt)
    for mnt in mounts:
        walk_root(mnt, "find")
except Exception:
    pass

# group safetensors by directory
from collections import defaultdict
ggufs = []
st = defaultdict(list)
for sz, fp, src in hits:
    if fp.lower().endswith(".gguf"):
        kind = "projector" if os.path.basename(fp).lower().startswith("mmproj") else "model"
        add({"name": os.path.splitext(os.path.basename(fp))[0], "path": fp, "bytes": sz,
             "format": "gguf", "source": src, "kind": kind})
    else:
        st[os.path.dirname(fp)].append((sz, fp, src))
for d, files in st.items():
    total = sum(x[0] for x in files)
    src = files[0][2]
    name = os.path.basename(d)
    path = d
    idx = os.path.join(d, "model.safetensors.index.json")
    if not os.path.isfile(idx) and len(files) == 1:
        path = files[0][1]
        name = os.path.splitext(os.path.basename(path))[0]
    add({"name": name, "path": path, "bytes": total, "format": "safetensors",
         "source": src, "kind": "model"})

weights.sort(key=lambda w: w.get("path", ""))
print(json.dumps({"weights": weights, "truncated": truncated}))
PY`
