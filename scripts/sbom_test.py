import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import json

spec = importlib.util.spec_from_file_location("sbom", Path(__file__).with_name("sbom.py"))
sbom = importlib.util.module_from_spec(spec)
spec.loader.exec_module(sbom)


class SBOMTests(unittest.TestCase):
    def fixture(self, root):
        files = {"go.mod": "module fixture\n", "go.sum": "", "web/package-lock.json": '{"packages": {}}',
                 "internal/img/codec_sources.json": '{"components": []}',
                 "internal/jxlcodec/lib/codec.wasm.gz": "jxl", "internal/colorcms/lcms2.wasm": "lcms", "binary": "binary"}
        for name, text in files.items():
            path = root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(text)
        manifest = {"sha256": sbom.digest(root / "internal/jxlcodec/lib/codec.wasm.gz"), "libjxl": "0.11.2", "revision": "abc", "sources": {"lcms": "def"}}
        (root / "internal/jxlcodec/lib/manifest.json").write_text(json.dumps(manifest))

    def test_module_replacement_and_allowlisted_build_settings(self):
        modules, settings = sbom.build_modules("fixture: go1.27.4\n\tmod\texample/main\t(devel)\t\n\tdep\texample/old\tv1.0.0\th1:old\n\t=>\texample/new\tv2.0.0\th1:new\n\tbuild\tGOARCH=arm64\n\tbuild\t-ldflags=contains-private-token\n\tbuild\tvcs.revision=abc\n")
        self.assertEqual(modules[1], {"path": "example/new", "version": "v2.0.0", "sum": "h1:new", "replaces": "example/old"})
        self.assertEqual(settings, {"GOARCH": "arm64", "vcs.revision": "abc"})

    def test_hash_binds_actual_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "binary"
            path.write_bytes(b"abc")
            self.assertEqual(sbom.digest(path), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")
            path.write_bytes(b"abd")
            self.assertNotEqual(sbom.digest(path), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad")

    def test_output_is_deterministic_and_runtime_snapshot_is_allowlisted(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixture(root)
            about = root / "about.json"
            about.write_text(json.dumps({"state_dir": "/private/should-not-appear", "admin_token": "secret-should-not-appear", "runtime_components": [{"id": "native-codec", "name": "codec", "version": "1.2.3", "execution": "native", "source": "actual-version-api"}]}))
            with patch.object(sbom, "go_command", return_value="fixture: go1.27.4\n\tmod\tfixture\tv1.0.0\n\tbuild\tGOARCH=amd64\n"):
                one = json.dumps(sbom.generate(root / "binary", root, "go", about), sort_keys=True)
                two = json.dumps(sbom.generate(root / "binary", root, "go", about), sort_keys=True)
                self.assertEqual(one, two)
                self.assertIn("actual-version-api", one)
                self.assertNotIn("should-not-appear", one)
                self.assertNotIn('"timestamp"', one)
                (root / "internal/jxlcodec/lib/codec.wasm.gz").write_bytes(b"tampered")
                with self.assertRaisesRegex(ValueError, "differs from its build manifest"):
                    sbom.generate(root / "binary", root, "go")

    def test_codec_replacement_cannot_reuse_old_provenance(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            self.fixture(root)
            (root / "internal/img/codec_sources.json").write_text('{"components": [{"module": "github.com/gen2brain/avif"}]}')
            build = "fixture: go1.27.4\n\tmod\tfixture\t(devel)\n\tdep\tgithub.com/gen2brain/avif\tv0.6.0\n\t=>\texample/custom-avif\tv1.0.0\n"
            with patch.object(sbom, "go_command", return_value=build):
                with self.assertRaisesRegex(ValueError, "replacement needs a reviewed inventory"):
                    sbom.generate(root / "binary", root, "go")


if __name__ == "__main__":
    unittest.main()
