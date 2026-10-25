# Static runtime notices

These texts accompany release binaries that statically link C runtimes.

Linux (musl, GCC runtime support), copied from PocketContext's
`scripts/licenses` at commit 0cbee4f6963c76453abf108602efc6de71c4156d:

- musl 1.2.6: <https://git.musl-libc.org/cgit/musl/plain/COPYRIGHT?h=v1.2.6>
- GCC 15.2.0 runtime exception: <https://github.com/gcc-mirror/gcc/blob/releases/gcc-15.2.0/COPYING.RUNTIME>
- GCC 15.2.0 GPL text: <https://github.com/gcc-mirror/gcc/blob/releases/gcc-15.2.0/COPYING3>

Windows (MinGW-w64 CRT, winpthreads, GCC runtime support), retrieved unchanged
from mingw-w64 commit 63ebe6cdb3ec4d5c586b7f0f0c03dd502e44c296:

- `COPYING.MinGW-w64-runtime/COPYING.MinGW-w64-runtime.txt`
- `mingw-w64-libraries/winpthreads/COPYING`

Review these notices when changing the Alpine builder or the Windows compiler.
`scripts/release.py` also collects license files from linked Go modules, the Go
toolchain, the vendored PocketContext server and the reader's production npm
packages into `THIRD_PARTY_NOTICES.txt`.
