package facts

// DiskWeightsDiscoveryScript inventories on-disk weights on a remote node.
// Marker axis-disk-weights-v1 is the fake-executor contains key.
// Python is best-effort: missing interpreter yields an empty scan, not a collect failure.
const DiskWeightsDiscoveryScript = `python3 - <<'PY'
# axis-disk-weights-v1
import json, os, sys, threading, time
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

# macOS iCloud placeholders: reading one downloads it, and a readdir can block
# for minutes. lstat flags are local, so check before any open or descend.
SF_DATALESS = 0x40000000

def dataless(path):
    try:
        return bool(getattr(os.lstat(path), "st_flags", 0) & SF_DATALESS)
    except OSError:
        return False

# Core AI / Core ML bundles: directories, recorded with kind unknown.
BUNDLES = {".aimodel": "aimodel", ".mlpackage": "coreml", ".mlmodelc": "coreml"}

def bundle_bytes(path):
    total, entries = 0, 0
    for root, dirs, files in os.walk(path):
        dirs[:] = [d for d in dirs if not dataless(os.path.join(root, d))]
        for fn in files:
            entries += 1
            if entries > 2000:
                return total
            fp = os.path.join(root, fn)
            if dataless(fp):
                continue
            try:
                total += os.lstat(fp).st_size
            except OSError:
                pass
    return total

def is_lfs_pointer(path):
    try:
        with open(path, "rb") as f:
            return f.read(64).startswith(b"version https://git-lfs")
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
            if not snap.is_dir() or dataless(str(e)):
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
                    if sz < MIN:
                        continue
                    if low.endswith(".safetensors") and is_lfs_pointer(fp):
                        continue
                    if low.endswith(".gguf"):
                        if not gguf_magic(fp):
                            continue
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
            # as_posix() avoids a backslash string that fish-unquoted heredocs break.
            rel = p.relative_to(man).as_posix()
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
hit_count = 0
DEADLINE = time.time() + 8

def is_network_source(device):
    # Keep in sync with facts.DFSourceIsNetwork.
    if not device:
        return False
    d = device.lower()
    if device.startswith("//") or ":/" in device:
        return True
    return any(tok in d for tok in ("nfs", "cifs", "smb", "sshfs", "rclone", "9p", "afs", "ceph", "gluster"))

def skip_device_mount(device, mnt):
    d = device.lower()
    if d in ("tmpfs", "devtmpfs", "devfs", "overlay", "squashfs", "proc", "sysfs",
             "cgroup", "cgroup2", "none", "udev", "dev", "efivarfs", "map"):
        return True
    if d.startswith("/dev/loop") or d.startswith("/dev/zram") or d.startswith("zram"):
        return True
    if mnt in ("/boot",) or mnt.startswith("/boot/") or mnt.startswith("/snap/"):
        return True
    if mnt.startswith("/var/lib/docker/") or mnt.startswith("/run/") or mnt.startswith("/sys/"):
        return True
    if is_network_source(device):
        return True
    return False

def walk_root(root, source):
    global truncated, hit_count
    if truncated or not os.path.isdir(root):
        return
    for dirpath, dirs, files in os.walk(root, topdown=True):
        if truncated or time.time() >= DEADLINE:
            truncated = True
            return
        if is_sys(dirpath):
            dirs[:] = []
            continue
        dirs[:] = [d for d in dirs if d not in SKIP and not dataless(os.path.join(dirpath, d))]
        for d in [d for d in dirs if os.path.splitext(d)[1].lower() in BUNDLES]:
            dirs.remove(d)
            if hit_count >= MAX_FILES:
                truncated = True
                return
            bp = os.path.join(dirpath, d)
            sz = bundle_bytes(bp)
            if sz >= MIN:
                hits.append((sz, bp, source))
                hit_count += 1
        for fn in files:
            if hit_count >= MAX_FILES or time.time() >= DEADLINE:
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
            if sz < MIN or dataless(fp):
                continue
            if low.endswith(".gguf") and not gguf_magic(fp):
                continue
            if low.endswith(".safetensors") and is_lfs_pointer(fp):
                continue
            hits.append((sz, fp, source))
            hit_count += 1

def walk_all():
    for p in (os.path.join(home, "models"), os.path.join(home, "Storage"), "/opt/models", "/models"):
        walk_root(p, "well-known")
    walk_mounts()

def walk_mounts():
    import subprocess
    try:
        df = subprocess.check_output(["df", "-kPl"], text=True, timeout=3)
    except Exception:
        return
    mounts = []
    for line in df.splitlines()[1:]:
        parts = line.split()
        if len(parts) < 6:
            continue
        device = parts[0]
        mnt = " ".join(parts[5:])
        if skip_device_mount(device, mnt):
            continue
        if mnt == "/" and home:
            mnt = home
        mounts.append(mnt)
    for mnt in mounts:
        walk_root(mnt, "find")

# DEADLINE is only checked between entries; a blocked readdir never returns.
# Walk in a daemon thread and stop waiting at HARD_LIMIT so the probe always ends.
HARD_LIMIT = 12
worker = threading.Thread(target=walk_all, daemon=True)
worker.start()
worker.join(HARD_LIMIT)
if worker.is_alive():
    truncated = True
hits = list(hits)

# group safetensors by directory
from collections import defaultdict
ggufs = []
st = defaultdict(list)
for sz, fp, src in hits:
    ext = os.path.splitext(fp)[1].lower()
    if ext in BUNDLES:
        add({"name": os.path.splitext(os.path.basename(fp))[0], "path": fp, "bytes": sz,
             "format": BUNDLES[ext], "source": src, "kind": "unknown"})
        continue
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
sys.stdout.flush()
os._exit(0)  # do not join a worker stuck in the kernel
PY`
