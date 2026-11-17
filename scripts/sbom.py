#!/usr/bin/env python3
"""Generate an offline, deterministic CycloneDX inventory for a built binary."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
from urllib.parse import quote


def digest(path):
    h = hashlib.sha256()
    with Path(path).open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            h.update(chunk)
    return h.hexdigest()


def go_command(go, *args):
    env = dict(os.environ, GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local")
    return subprocess.check_output([go, *args], env=env, text=True)


def codec_sources(go):
    packages = {
        "webp": ("lib/Makefile", "LIBWEBP_VERSION", ["lib/webp.wasm", "libwebp.go"]),
        "avif": ("lib/Makefile", "LIBAVIF_VERSION", ["lib/avif.wasm.gz", "libavif.go"]),
        "jpegli": ("lib/Makefile", "JPEGLI_VERSION", ["lib/jpegli.wasm.gz", "libjpegli.go"]),
    }
    result = []
    for name, (recipe, version_key, inputs) in packages.items():
        mod = json.loads(go_command(go, "list", "-m", "-json", "github.com/gen2brain/" + name))
        if mod.get("Replace"):
            raise ValueError("codec module replacement needs a reviewed inventory: " + name)
        root = Path(mod["Dir"])
        text = (root / recipe).read_text()
        declared = dict(re.findall(r"^([A-Z0-9_]+_VERSION)\s*=\s*(\S+)", text, re.M))
        version = declared[version_key].removeprefix("v")
        result.append({
            "id": name + "-embedded", "name": name, "module": mod["Path"],
            "module_version": mod["Version"], "module_sum": mod.get("Sum", ""),
            "version": "unreported-upstream" if version in ("main", "master", "stable") else version,
            "version_source": "pinned Go module build recipe; main/stable are not immutable upstream revisions",
            "declared_sources": declared,
            "inputs": [{"path": item, "sha256": digest(root / item)} for item in inputs],
        })
    return {"schema": 1, "components": result}


def build_modules(text):
    modules, settings = [], {}
    for line in text.splitlines():
        fields = line.strip().split("\t")
        if fields[0] in ("mod", "dep") and len(fields) >= 3:
            modules.append({"path": fields[1], "version": fields[2], "sum": fields[3] if len(fields) > 3 else ""})
        elif fields[0] == "=>" and len(fields) >= 3 and modules:
            original = modules[-1]["path"]
            modules[-1] = {"path": fields[1], "version": fields[2], "sum": fields[3] if len(fields) > 3 else "", "replaces": original}
        elif fields[0] == "build" and len(fields) > 1:
            key, _, value = fields[1].partition("=")
            if key in {"GOOS", "GOARCH", "CGO_ENABLED", "GOAMD64", "GOARM64", "vcs.revision", "vcs.modified", "-tags"}:
                settings[key] = value
    return modules, settings


def prop(name, value):
    return {"name": "midopress:" + name, "value": str(value)}


def component(name, version, reference, **kwargs):
    return {"type": "library", "bom-ref": reference, "name": name, "version": version, **kwargs}


def generate(binary, root, go, runtime_about=None, version=None):
    binary = Path(binary)
    build_text = go_command(go, "version", "-m", str(binary))
    modules, settings = build_modules(build_text)
    if not modules:
        raise ValueError("binary has no Go module build information")
    components = []
    toolchain = re.search(r":\s+(go\S+)", build_text.splitlines()[0])
    if toolchain:
        components.append(component("Go runtime / standard library", toolchain.group(1), "go-toolchain", properties=[prop("version_source", "binary Go build information")]))
    for mod in modules:
        identity = "pkg:golang/" + quote(mod["path"], safe="/") + "@" + quote(mod["version"], safe="")
        properties = [prop("go.module_sum", mod["sum"])] if mod["sum"] else []
        if mod.get("replaces"):
            properties.append(prop("go.replaces", mod["replaces"]))
        components.append(component(mod["path"], mod["version"], identity, purl=identity, properties=properties))

    recipe = json.loads((root / "internal/img/codec_sources.json").read_text())
    codec_modules = {c["module"] for c in recipe["components"]}
    for mod in modules:
        if mod.get("replaces") in codec_modules:
            raise ValueError("codec module replacement needs a reviewed inventory: " + mod["replaces"])
    included = {m["path"]: m["version"] for m in modules}
    for codec in recipe["components"]:
        if codec["module"] not in included:
            continue
        if codec["module_version"] != included[codec["module"]]:
            raise ValueError("codec recipe does not match binary Go dependency: " + codec["module"])
        properties = [prop("source.go_module", codec["module"] + "@" + codec["module_version"]), prop("version_source", codec["version_source"])]
        properties += [prop("source." + k, v) for k, v in sorted(codec["declared_sources"].items())]
        properties += [prop("input.sha256:" + item["path"], item["sha256"]) for item in codec["inputs"]]
        components.append(component(codec["name"], codec["version"], codec["id"], properties=properties))

    jxl_path = root / "internal/jxlcodec/lib/codec.wasm.gz"
    jxl = json.loads((root / "internal/jxlcodec/lib/manifest.json").read_text())
    if digest(jxl_path) != jxl["sha256"]:
        raise ValueError("libjxl module differs from its build manifest")
    components.append(component("libjxl", jxl["libjxl"], "jxl-wasi", hashes=[{"alg": "SHA-256", "content": jxl["sha256"]}], properties=[prop("execution", "wasm"), prop("source.commit", jxl["revision"])]))
    for name, revision in sorted(jxl["sources"].items()):
        components.append(component(name, revision, "jxl-" + name, properties=[prop("version_kind", "pinned upstream submodule commit"), prop("parent", "jxl-wasi")]))
    components.append(component("LittleCMS", "2.19.1", "lcms-wasi", hashes=[{"alg": "SHA-256", "content": digest(root / "internal/colorcms/lcms2.wasm")}], properties=[prop("execution", "wasm"), prop("source.tag", "lcms2.19.1")]))

    lock = json.loads((root / "web/package-lock.json").read_text())
    for path, package in sorted(lock.get("packages", {}).items()):
        if not path or not package.get("version"):
            continue
        name = package.get("name") or path.rsplit("node_modules/", 1)[-1]
        identity = "npm:" + path + "@" + package["version"]
        properties = [prop("scope", "frontend-build" if package.get("dev") else "frontend"), prop("npm.integrity", package.get("integrity", "unreported"))]
        components.append(component(name, package["version"], identity, properties=properties))

    if runtime_about:
        about = json.loads(Path(runtime_about).read_text())
        for entry in about.get("runtime_components", []):
            if entry.get("execution") != "native":
                continue
            properties = [prop("execution", "native"), prop("version_source", entry.get("source", "unreported")), prop("status", entry.get("status", "unreported")), prop("scope", "observed runtime host; not bundled by this artifact")]
            components.append(component(entry["name"], entry["version"], "runtime:" + entry["id"], properties=properties))

    main = {"type": "application", "bom-ref": "midopress-binary", "name": "midopress", "version": version or modules[0]["version"], "hashes": [{"alg": "SHA-256", "content": digest(binary)}]}
    properties = [prop("build." + k, v) for k, v in sorted(settings.items())]
    properties += [prop("source_input.sha256:" + path, digest(root / path)) for path in ["go.mod", "go.sum", "web/package-lock.json", "internal/img/codec_sources.json"]]
    properties.append(prop("coverage", "Go modules, embedded codec provenance and frontend lock; native entries only when supplied from same-runtime About; no claim to enumerate every OS transitive library"))
    return {"bomFormat": "CycloneDX", "specVersion": "1.6", "version": 1,
            "metadata": {"component": main, "properties": properties},
            "components": sorted(components, key=lambda c: c["bom-ref"]),
            "dependencies": [{"ref": "midopress-binary", "dependsOn": sorted(c["bom-ref"] for c in components)}]}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("binary", nargs="?")
    parser.add_argument("--go", default=os.environ.get("GO", "go"))
    parser.add_argument("--root", type=Path, default=Path(__file__).resolve().parent.parent)
    parser.add_argument("--runtime-about", type=Path)
    parser.add_argument("--version")
    parser.add_argument("--output", type=Path)
    parser.add_argument("--refresh-codecs", action="store_true")
    parser.add_argument("--verify-codecs", action="store_true")
    args = parser.parse_args()
    if args.refresh_codecs or args.verify_codecs:
        data = codec_sources(args.go)
        path = args.root / "internal/img/codec_sources.json"
        if args.verify_codecs:
            if json.loads(path.read_text()) != data:
                raise SystemExit("codec source inventory is stale; review dependencies, then --refresh-codecs")
            print("codec source inventory verified")
            return
        args.output = path
    elif args.binary:
        data = generate(args.binary, args.root, args.go, args.runtime_about, args.version)
    else:
        parser.error("a binary or --refresh-codecs/--verify-codecs is required")
    text = json.dumps(data, indent=2, sort_keys=True, ensure_ascii=True) + "\n"
    if args.output:
        args.output.write_text(text)
    else:
        print(text, end="")


if __name__ == "__main__":
    main()
