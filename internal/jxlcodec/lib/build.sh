#!/usr/bin/env bash
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
sdk=${WASI_SDK_PATH:-/opt/wasi-sdk}
work=${JXL_BUILD_DIR:-/tmp/midopress-jxl-wasm}
src="$work/libjxl"
build="$work/build"
if [ ! -d "$src/.git" ]; then
  git clone --depth 1 --branch v0.11.2 https://github.com/libjxl/libjxl.git "$src"
fi
test "$(git -C "$src" rev-parse HEAD)" = 332feb17d17311c748445f7ee75c4fb55cc38530
git -C "$src" submodule update --init --depth 1 third_party/brotli third_party/highway third_party/lcms
python3 - "$src" <<'PY'
from pathlib import Path
import sys
p=Path(sys.argv[1])
f=p/'CMakeLists.txt'
s=f.read_text().replace('set(THREADS_PREFER_PTHREAD_FLAG YES)','').replace('find_package(Threads REQUIRED)','')
f.write_text(s)
f=p/'lib/CMakeLists.txt';f.write_text(f.read_text().replace('include(jxl_threads.cmake)',''))
for name in ['jxl.cmake','jpegli.cmake']:
 f=p/'lib'/name;f.write_text('\n'.join(x for x in f.read_text().split('\n') if 'Threads::Threads' not in x))
PY
cmake -S "$src" -B "$build" \
  -DCMAKE_TOOLCHAIN_FILE="$sdk/share/cmake/wasi-sdk.cmake" \
  -DCMAKE_BUILD_TYPE=MinSizeRel -DBUILD_SHARED_LIBS=OFF -DBUILD_TESTING=OFF \
  -DJPEGXL_ENABLE_TOOLS=OFF -DJPEGXL_ENABLE_DEVTOOLS=OFF -DJPEGXL_ENABLE_JPEGLI=OFF \
  -DJPEGXL_ENABLE_JPEGLI_LIBJPEG=OFF -DJPEGXL_ENABLE_DOXYGEN=OFF -DJPEGXL_ENABLE_MANPAGES=OFF \
  -DJPEGXL_ENABLE_BENCHMARK=OFF -DJPEGXL_ENABLE_EXAMPLES=OFF -DJPEGXL_ENABLE_SJPEG=OFF \
  -DJPEGXL_ENABLE_WASM_THREADS=OFF -DJPEGXL_ENABLE_JNI=OFF -DJPEGXL_ENABLE_TRANSCODE_JPEG=ON \
  -DJPEGXL_ENABLE_BOXES=ON -DJPEGXL_ENABLE_SKCMS=OFF \
  -DCMAKE_CXX_FLAGS='-msimd128 -fno-exceptions -fno-rtti -DHWY_COMPILE_ONLY_SCALAR'
cmake --build "$build" --target jxl jxl_cms --parallel "${JXL_BUILD_JOBS:-2}"
"$sdk/bin/clang" -O3 -mexec-model=reactor -msimd128 -fno-exceptions \
  -Wl,--no-entry -Wl,--strip-debug -Wl,--max-memory=536870912 \
  -Wl,--export=malloc -Wl,--export=free -Wl,--export=mp_recompress \
  -Wl,--export=mp_reconstruct -Wl,--export=mp_inspect -Wl,--export=mp_decode \
  -Wl,--export=mp_encode -I"$build/lib/include" -I"$src/lib/include" \
  "$here/codec.c" "$build/lib/libjxl.a" "$build/lib/libjxl_cms.a" \
  "$build/third_party/highway/libhwy.a" "$build/third_party/brotli/libbrotlienc.a" \
  "$build/third_party/brotli/libbrotlidec.a" "$build/third_party/brotli/libbrotlicommon.a" \
  "$build/third_party/liblcms2.a" -lc++ -lc++abi -o "$work/codec.wasm"
gzip -n -9 -c "$work/codec.wasm" > "$here/codec.wasm.gz"
mkdir -p "$here/licenses"
cp "$src/LICENSE" "$here/licenses/libjxl.txt"
cp "$src/third_party/brotli/LICENSE" "$here/licenses/brotli.txt"
cp "$src/third_party/highway/LICENSE" "$here/licenses/highway.txt"
cp "$src/third_party/lcms/LICENSE" "$here/licenses/lcms.txt"
python3 - "$here" "$src" "$sdk" <<'PY'
import hashlib,json,subprocess,sys
from pathlib import Path
p,src,sdk=map(Path,sys.argv[1:])
revision=lambda d:subprocess.check_output(['git','-C',str(d),'rev-parse','HEAD'],text=True).strip()
manifest={'libjxl':'0.11.2','revision':revision(src),'target':'wasm32-wasi',
          'wasi_sdk':(sdk/'VERSION').read_text().splitlines()[0],
          'sha256':hashlib.sha256((p/'codec.wasm.gz').read_bytes()).hexdigest(),
          'wrapper_sha256':hashlib.sha256((p/'codec.c').read_bytes()).hexdigest(),
          'sources':{name:revision(src/'third_party'/name) for name in ['brotli','highway','lcms']}}
(p/'manifest.json').write_text(json.dumps(manifest,indent=2)+'\n')
PY
sha256sum "$here/codec.wasm.gz"
