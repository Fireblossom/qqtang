"""Minimal Win32 user-mode emulator for running isolated Client.exe functions.

Maps a 32-bit PE image into Unicorn, points every IAT slot at a stub that is
implemented in Python, and lets callers invoke a function with cdecl/thiscall
arguments. Only the imports reached by the functions we run are implemented;
any other import stops emulation with its name so it can be added.
"""

from __future__ import annotations

import pathlib
import struct
import zlib

import pefile
from unicorn import UC_ARCH_X86, UC_HOOK_BLOCK, UC_HOOK_CODE, UC_HOOK_MEM_INVALID, UC_HOOK_MEM_READ, UC_MODE_32, UC_PROT_ALL, Uc, UcError
from unicorn.x86_const import (UC_X86_REG_EAX, UC_X86_REG_EBP, UC_X86_REG_ECX, UC_X86_REG_EDX, UC_X86_REG_EIP,
                               UC_X86_REG_DS, UC_X86_REG_ES, UC_X86_REG_ESP, UC_X86_REG_FS, UC_X86_REG_GDTR,
                               UC_X86_REG_SS)

GDT = 0x00100000
TEB = 0x00102000
PEB = 0x00104000
STUBS = 0x00200000
RET_MAGIC = 0x002FFFF0
STACK = 0x02000000
STACK_SIZE = 0x00200000
HEAP = 0x30000000
HEAP_SIZE = 0x04000000
DATA = 0x0F000000          # data imports (npos, _Nullstr, ...)

U32 = struct.Struct("<I")


class EmuStop(Exception):
    pass


def _align(n: int, a: int = 0x1000) -> int:
    return (n + a - 1) & ~(a - 1)


def _gdt_entry(base: int, limit: int, access: int, flags: int) -> bytes:
    v = ((limit & 0xFFFF) | ((base & 0xFFFFFF) << 16) | (access << 40) | (((limit >> 16) & 0xF) << 48)
         | (flags << 52) | ((base >> 24) << 56))
    return struct.pack("<Q", v)


class Win32Emu:
    def __init__(self, exe: pathlib.Path, root: pathlib.Path, trace: bool = False):
        self.root = root
        self.trace = trace
        self.uc = Uc(UC_ARCH_X86, UC_MODE_32)
        self.pe = pefile.PE(str(exe), fast_load=False)
        self.handlers: dict[str, tuple] = {}
        self.stub_names: dict[int, str] = {}
        self.heap_next = HEAP
        self.heap_sizes: dict[int, int] = {}
        self.free_lists: dict[int, list[int]] = {}
        self.files: dict[int, list] = {}      # handle -> [data, pos]
        self.next_handle = 0x100
        self.last_error = 0
        self.log: list[str] = []
        self._register_default_handlers()
        self._load()

    # ------------------------------------------------------------------ memory
    def r32(self, a: int) -> int:
        return U32.unpack(self.uc.mem_read(a, 4))[0]

    def w32(self, a: int, v: int) -> None:
        self.uc.mem_write(a, U32.pack(v & 0xFFFFFFFF))

    def read(self, a: int, n: int) -> bytes:
        return bytes(self.uc.mem_read(a, n))

    def write(self, a: int, b: bytes) -> None:
        self.uc.mem_write(a, bytes(b))

    def cstr(self, a: int, limit: int = 1 << 20) -> bytes:
        out = bytearray()
        while len(out) < limit:
            chunk = self.uc.mem_read(a + len(out), 256)
            i = chunk.find(0)
            if i >= 0:
                out += chunk[:i]
                return bytes(out)
            out += chunk
        return bytes(out)

    def malloc(self, n: int) -> int:
        """Zero-filled allocation from power-of-two size classes; freed blocks are reused."""
        cap = 16
        while cap < n:
            cap <<= 1
        pool = self.free_lists.setdefault(cap, [])
        if pool:
            a = pool.pop()
        else:
            a = self.heap_next
            self.heap_next += cap
            if self.heap_next >= HEAP + HEAP_SIZE:
                raise EmuStop("heap exhausted")
        self.heap_sizes[a] = cap
        self.write(a, b"\0" * cap)
        return a

    def free(self, a: int) -> None:
        cap = self.heap_sizes.pop(a, None)
        if cap is not None:
            self.free_lists[cap].append(a)

    def strdup(self, b: bytes) -> int:
        a = self.malloc(len(b) + 1)
        self.write(a, b + b"\0")
        return a

    # ----------------------------------------------------------------- loading
    def _load(self) -> None:
        pe, uc = self.pe, self.uc
        base = pe.OPTIONAL_HEADER.ImageBase
        size = _align(pe.OPTIONAL_HEADER.SizeOfImage)
        self.base = base
        uc.mem_map(base, size, UC_PROT_ALL)
        uc.mem_write(base, pe.header)
        for s in pe.sections:
            raw = s.get_data()[: s.Misc_VirtualSize or s.SizeOfRawData]
            uc.mem_write(base + s.VirtualAddress, raw)

        uc.mem_map(GDT, 0x1000)
        uc.mem_map(TEB, 0x4000)
        uc.mem_map(STUBS, 0x100000)
        uc.mem_map(STACK, STACK_SIZE)
        uc.mem_map(HEAP, HEAP_SIZE)
        uc.mem_map(DATA, 0x10000)
        uc.mem_write(GDT + 8 * 2, _gdt_entry(0, 0xFFFFF, 0x92, 0xC))     # flat 32-bit data (stack must be 32-bit)
        uc.mem_write(GDT + 8 * 3, _gdt_entry(TEB, 0xFFF, 0x92, 0x4))
        uc.reg_write(UC_X86_REG_GDTR, (0, GDT, 0xFFF, 0))
        uc.reg_write(UC_X86_REG_SS, 2 << 3)
        uc.reg_write(UC_X86_REG_DS, 2 << 3)
        uc.reg_write(UC_X86_REG_ES, 2 << 3)
        uc.reg_write(UC_X86_REG_FS, 3 << 3)
        self.w32(TEB + 0x00, 0xFFFFFFFF)
        self.w32(TEB + 0x04, STACK + STACK_SIZE)
        self.w32(TEB + 0x08, STACK)
        self.w32(TEB + 0x18, TEB)
        self.w32(TEB + 0x30, PEB)
        self.w32(PEB + 0x08, base)

        data_next = DATA
        idx = 0
        for entry in pe.DIRECTORY_ENTRY_IMPORT:
            dll = entry.dll.decode()
            for imp in entry.imports:
                name = imp.name.decode() if imp.name else f"{dll}#{imp.ordinal}"
                if name.endswith("@2IB") or name.startswith("?npos"):      # static const npos
                    self.w32(data_next, 0xFFFFFFFF)
                    self.w32(imp.address, data_next)
                    data_next += 16
                    continue
                if name.startswith("?_C@?1??_Nullstr"):
                    self.w32(imp.address, data_next)
                    data_next += 16
                    continue
                stub = STUBS + idx * 16
                idx += 1
                self.stub_names[stub] = name
                uc.mem_write(stub, b"\xF4")    # hlt: never actually executed
                self.w32(imp.address, stub)
        self.stub_end = STUBS + idx * 16
        uc.mem_write(RET_MAGIC, b"\xF4")
        uc.hook_add(UC_HOOK_CODE, self._on_stub, begin=STUBS, end=STUBS + 0xFFFFF)
        uc.hook_add(UC_HOOK_MEM_INVALID, self._on_bad_mem)

    def _on_bad_mem(self, uc, access, address, size, value, user):
        eip = uc.reg_read(UC_X86_REG_EIP)
        self.log.append(f"bad memory access type={access} addr={address:#x} size={size} eip={eip:#x}")
        self.log.append("stack return addresses: " + " ".join(f"{a:#x}" for a in self.stack_code_refs()))
        return False

    def trace_blocks(self, lo: int, hi: int) -> list:
        """Record (from_block, to_block) whenever execution enters [lo, hi) from outside it."""
        out: list = []
        state = {"prev": 0}

        def hook(uc, address, size, user):
            p = state["prev"]
            if lo <= address < hi and not (lo <= p < hi):
                out.append((p, address))
            state["prev"] = address

        self.uc.hook_add(UC_HOOK_BLOCK, hook)
        return out

    def trace_reads(self, lo: int, hi: int) -> list:
        """Record (eip, address, size) for every read in [lo, hi)."""
        out: list = []

        def hook(uc, access, address, size, value, user):
            out.append((uc.reg_read(UC_X86_REG_EIP), address, size, self.r32(address) if size == 4 else None))

        self.uc.hook_add(UC_HOOK_MEM_READ, hook, begin=lo, end=hi - 1)
        return out

    def stack_code_refs(self, depth: int = 0x800) -> list[int]:
        """Dwords on the stack that point into the image's code section (likely return addresses)."""
        esp = self.uc.reg_read(UC_X86_REG_ESP)
        top = STACK + STACK_SIZE
        n = min(depth, top - esp) // 4
        words = struct.unpack(f"<{n}I", self.read(esp, n * 4))
        code = [(self.base + s.VirtualAddress, self.base + s.VirtualAddress + s.Misc_VirtualSize)
                for s in self.pe.sections if s.Characteristics & 0x20000000]
        return [w for w in words if any(lo <= w < hi for lo, hi in code)]

    def add_stub(self, name: str) -> int:
        stub = self.stub_end
        self.stub_end += 16
        self.stub_names[stub] = name
        self.uc.mem_write(stub, b"\xF4")
        return stub

    def make_object(self, name: str, nslots: int = 64, size: int = 0x100) -> int:
        """Allocate an object whose vtable slots are stubs named '<name>+0xNN' (byte offset)."""
        vt = self.malloc(nslots * 4)
        for i in range(nslots):
            self.w32(vt + 4 * i, self.add_stub(f"{name}+{4 * i:#x}"))
        obj = self.malloc(size)
        self.w32(obj, vt)
        return obj

    # ----------------------------------------------------------------- calling
    def arg(self, i: int) -> int:
        return self.r32(self.uc.reg_read(UC_X86_REG_ESP) + 4 + 4 * i)

    def _on_stub(self, uc, address, size, user):
        if address == RET_MAGIC:
            return
        name = self.stub_names.get(address)
        esp = uc.reg_read(UC_X86_REG_ESP)
        ret = self.r32(esp)
        h = self.handlers.get(name)
        if h is None:
            args = " ".join(f"{self.r32(esp + 4 + 4 * i):#x}" for i in range(6))
            ecx = uc.reg_read(UC_X86_REG_ECX)
            self.stop_reason = f"unimplemented import {name} (called from {ret:#x}; ecx={ecx:#x} stack args {args})"
            uc.emu_stop()
            return
        fn, npop = h
        if self.trace:
            self.log.append(f"{ret:#010x} -> {name}")
        self.override = None
        try:
            rv = fn(self)
        except EmuStop as e:
            self.stop_reason = f"{name}: {e}"
            uc.emu_stop()
            return
        if rv is not None:
            uc.reg_write(UC_X86_REG_EAX, rv & 0xFFFFFFFF)
        new_esp, new_eip = self.override or (esp + 4 + 4 * npop, ret)
        uc.reg_write(UC_X86_REG_ESP, new_esp)
        uc.reg_write(UC_X86_REG_EIP, new_eip)

    def call(self, addr: int, args=(), ecx: int | None = None, limit: int = 50_000_000) -> int:
        esp = STACK + STACK_SIZE - 0x1000
        for a in reversed(args):
            esp -= 4
            self.w32(esp, a)
        esp -= 4
        self.w32(esp, RET_MAGIC)
        self.uc.reg_write(UC_X86_REG_ESP, esp)
        self.uc.reg_write(UC_X86_REG_EBP, 0)
        if ecx is not None:
            self.uc.reg_write(UC_X86_REG_ECX, ecx)
        self.stop_reason = None
        try:
            self.uc.emu_start(addr, RET_MAGIC, count=limit)
        except UcError as e:
            eip = self.uc.reg_read(UC_X86_REG_EIP)
            raise EmuStop(f"{e} at eip={eip:#x}; " + "; ".join(self.log[-3:])) from None
        if self.stop_reason:
            raise EmuStop(self.stop_reason)
        eip = self.uc.reg_read(UC_X86_REG_EIP)
        if eip != RET_MAGIC:
            raise EmuStop(f"stopped at {eip:#x} before returning (instruction limit?)")
        return self.uc.reg_read(UC_X86_REG_EAX)

    # ---------------------------------------------------------------- handlers
    def handler(self, name: str, npop: int = 0):
        def deco(fn):
            self.handlers[name] = (fn, npop)
            return fn
        return deco

    def open_file(self, path: bytes) -> bytes | None:
        p = path.decode("latin-1").replace("\\", "/")
        f = self.root / p
        if not f.is_file():
            return None
        return f.read_bytes()

    # VC6 std::string: {allocator byte, char *ptr, size_t len, size_t res}; ptr[-1] is a refcount.
    def str_get(self, s: int) -> bytes:
        ptr, n = self.r32(s + 4), self.r32(s + 8)
        return self.read(ptr, n) if ptr else b""

    def str_set(self, s: int, b: bytes) -> None:
        buf = self.malloc(len(b) + 2)
        self.write(buf, b"\0" + b + b"\0")
        self.w32(s + 4, buf + 1)
        self.w32(s + 8, len(b))
        self.w32(s + 12, len(b))

    def str_init(self, s: int, b: bytes = b"") -> None:
        self.write(s, b"\0" * 16)
        if b:
            self.str_set(s, b)

    def _register_default_handlers(self) -> None:
        h = self.handler
        S = "?$basic_string@DU?$char_traits@D@std@@V?$allocator@D@2@@std@@"

        @h("_EH_prolog")
        def _(e):
            # push -1; push eax; push fs:[0]; fs:[0] = esp; swap ebp with return address; jump back
            uc = e.uc
            esp = uc.reg_read(UC_X86_REG_ESP)
            ret = e.r32(esp)
            eax = uc.reg_read(UC_X86_REG_EAX)
            ebp = uc.reg_read(UC_X86_REG_EBP)
            e.w32(esp, ebp)                       # [esp] = saved ebp (was return address)
            e.w32(esp - 4, 0xFFFFFFFF)
            e.w32(esp - 8, eax)
            e.w32(esp - 12, e.r32(TEB))
            e.w32(TEB, esp - 12)
            uc.reg_write(UC_X86_REG_EBP, esp)
            e.override = (esp - 12, ret)
            return None

        @h("??2@YAPAXI@Z")
        def _(e):
            return e.malloc(e.arg(0))

        @h("malloc")
        def _(e):
            return e.malloc(e.arg(0))

        @h("free")
        def _(e):
            e.free(e.arg(0))
            return 0

        self.handlers["??3@YAXPAX@Z"] = self.handlers["free"]

        @h("calloc")
        def _(e):
            return e.malloc(e.arg(0) * e.arg(1))

        @h("memcpy")
        def _(e):
            d, s, n = e.arg(0), e.arg(1), e.arg(2)
            if n:
                e.write(d, e.read(s, n))
            return d

        self.handlers["memmove"] = self.handlers["memcpy"]

        @h("memset")
        def _(e):
            d, c, n = e.arg(0), e.arg(1), e.arg(2)
            if n:
                e.write(d, bytes([c & 0xFF]) * n)
            return d

        @h("memcmp")
        def _(e):
            a, b = e.read(e.arg(0), e.arg(2)), e.read(e.arg(1), e.arg(2))
            return (a > b) - (a < b)

        @h("strlen")
        def _(e):
            return len(e.cstr(e.arg(0)))

        @h("strcpy")
        def _(e):
            e.write(e.arg(0), e.cstr(e.arg(1)) + b"\0")
            return e.arg(0)

        @h("strncpy")
        def _(e):
            s = e.cstr(e.arg(1))[: e.arg(2)]
            e.write(e.arg(0), s.ljust(e.arg(2), b"\0"))
            return e.arg(0)

        @h("strcat")
        def _(e):
            d = e.cstr(e.arg(0))
            e.write(e.arg(0) + len(d), e.cstr(e.arg(1)) + b"\0")
            return e.arg(0)

        @h("strcmp")
        def _(e):
            a, b = e.cstr(e.arg(0)), e.cstr(e.arg(1))
            return (a > b) - (a < b)

        @h("strncmp")
        def _(e):
            n = e.arg(2)
            a, b = e.cstr(e.arg(0))[:n], e.cstr(e.arg(1))[:n]
            return (a > b) - (a < b)

        @h("_strnicmp")
        def _(e):
            n = e.arg(2)
            a, b = e.cstr(e.arg(0))[:n].lower(), e.cstr(e.arg(1))[:n].lower()
            return (a > b) - (a < b)

        @h("_strcmpi")
        def _(e):
            a, b = e.cstr(e.arg(0)).lower(), e.cstr(e.arg(1)).lower()
            return (a > b) - (a < b)

        @h("strchr")
        def _(e):
            s = e.cstr(e.arg(0))
            c = e.arg(1) & 0xFF
            if c == 0:
                return e.arg(0) + len(s)
            i = s.find(bytes([c]))
            return e.arg(0) + i if i >= 0 else 0

        @h("strrchr")
        def _(e):
            s = e.cstr(e.arg(0))
            i = s.rfind(bytes([e.arg(1) & 0xFF]))
            return e.arg(0) + i if i >= 0 else 0

        @h("strstr")
        def _(e):
            i = e.cstr(e.arg(0)).find(e.cstr(e.arg(1)))
            return e.arg(0) + i if i >= 0 else 0

        @h("tolower")
        def _(e):
            c = e.arg(0)
            return c + 32 if 65 <= c <= 90 else c

        @h("isspace")
        def _(e):
            return 1 if e.arg(0) in (9, 10, 11, 12, 13, 32) else 0

        @h("isalpha")
        def _(e):
            c = e.arg(0)
            return 1 if 65 <= c <= 90 or 97 <= c <= 122 else 0

        @h("isalnum")
        def _(e):
            c = e.arg(0)
            return 1 if 65 <= c <= 90 or 97 <= c <= 122 or 48 <= c <= 57 else 0

        @h("isupper")
        def _(e):
            return 1 if 65 <= e.arg(0) <= 90 else 0

        @h("atoi")
        def _(e):
            return _atoi(e.cstr(e.arg(0)))

        h("atol")(lambda e: _atoi(e.cstr(e.arg(0))))

        @h("sprintf")
        def _(e):
            out = _format(e, e.cstr(e.arg(1)), 2)
            e.write(e.arg(0), out + b"\0")
            return len(out)

        @h("sscanf")
        def _(e):
            n = _scan(e, e.cstr(e.arg(0)), e.cstr(e.arg(1)), 2)
            e.log.append(f"sscanf {e.cstr(e.arg(0))[:40]!r} {e.cstr(e.arg(1))!r} -> {n}")
            return n

        @h("_snprintf")
        def _(e):
            out = _format(e, e.cstr(e.arg(2)), 3)[: e.arg(1)]
            e.write(e.arg(0), out + (b"\0" if len(out) < e.arg(1) else b""))
            return len(out)

        # ---- stdio on the client directory
        @h("fopen")
        def _(e):
            data = e.open_file(e.cstr(e.arg(0)))
            e.log.append(f"fopen {e.cstr(e.arg(0))!r} mode={e.cstr(e.arg(1))!r} -> {'ok' if data is not None else 'missing'}")
            if data is None:
                return 0
            hnd = e.next_handle
            e.next_handle += 0x10
            e.files[hnd] = [data, 0]
            return hnd

        @h("fclose")
        def _(e):
            e.files.pop(e.arg(0), None)
            return 0

        @h("fseek")
        def _(e):
            f = e.files[e.arg(0)]
            off = struct.unpack("<i", U32.pack(e.arg(1)))[0]
            whence = e.arg(2)
            f[1] = [off, f[1] + off, len(f[0]) + off][whence]
            return 0

        @h("ftell")
        def _(e):
            return e.files[e.arg(0)][1]

        @h("fread")
        def _(e):
            buf, size, cnt, hnd = e.arg(0), e.arg(1), e.arg(2), e.arg(3)
            f = e.files[hnd]
            n = min(size * cnt, len(f[0]) - f[1])
            n -= n % size if size else 0
            e.write(buf, f[0][f[1]: f[1] + n])
            f[1] += n
            return n // size if size else 0

        @h("fprintf")
        def _(e):
            return 0

        @h("fflush")
        def _(e):
            return 0

        # ---- kernel32
        @h("CreateFileA", 7)
        def _(e):
            data = e.open_file(e.cstr(e.arg(0)))
            e.log.append(f"CreateFileA {e.cstr(e.arg(0))!r} -> {'ok' if data is not None else 'missing'}")
            if data is None:
                e.last_error = 2
                return 0xFFFFFFFF
            hnd = e.next_handle
            e.next_handle += 0x10
            e.files[hnd] = [data, 0]
            return hnd

        @h("GetFileSize", 2)
        def _(e):
            if e.arg(1):
                e.w32(e.arg(1), 0)
            return len(e.files[e.arg(0)][0])

        @h("CloseHandle", 1)
        def _(e):
            e.files.pop(e.arg(0), None)
            return 1

        @h("CreateFileMappingA", 6)
        def _(e):
            hnd = e.next_handle
            e.next_handle += 0x10
            e.files[hnd] = e.files[e.arg(0)]
            return hnd

        @h("MapViewOfFile", 5)
        def _(e):
            data = e.files[e.arg(0)][0]
            a = e.malloc(len(data) + 1)
            e.write(a, data)
            return a

        @h("UnmapViewOfFile", 1)
        def _(e):
            return 1

        @h("GetPrivateProfileIntA", 4)
        def _(e):
            sec, key, dflt, fn = e.cstr(e.arg(0)), e.cstr(e.arg(1)), e.arg(2), e.cstr(e.arg(3))
            v = _ini_get(e, fn, sec, key)
            e.log.append(f"GetPrivateProfileInt {fn!r} [{sec!r}] {key!r} default={dflt} -> {v!r}")
            return _atoi(v) if v is not None else dflt

        @h("GetPrivateProfileStringA", 6)
        def _(e):
            sec, key, dflt, buf, size, fn = (e.cstr(e.arg(0)), e.cstr(e.arg(1)), e.cstr(e.arg(2)) if e.arg(2) else b"",
                                             e.arg(3), e.arg(4), e.cstr(e.arg(5)))
            v = _ini_get(e, fn, sec, key)
            e.log.append(f"GetPrivateProfileString {fn!r} [{sec!r}] {key!r} default={dflt!r} -> {v!r}")
            v = (dflt if v is None else v)[: size - 1]
            e.write(buf, v + b"\0")
            return len(v)

        @h("GetLastError", 0)
        def _(e):
            return e.last_error

        @h("GetTickCount", 0)
        def _(e):
            return 1000000

        h("timeGetTime", 0)(lambda e: 1000000)

        @h("OutputDebugStringA", 1)
        def _(e):
            e.log.append("OutputDebugString " + e.cstr(e.arg(0)).decode("latin-1"))
            return 0

        def new_handle(e):
            e.next_handle += 0x10
            return e.next_handle

        for n, npop in (("CreateSemaphoreA", 4), ("CreateMutexA", 3), ("CreateEventA", 4)):
            h(n, npop)(new_handle)
        for n, npop in (("ReleaseSemaphore", 3), ("ReleaseMutex", 1), ("SetEvent", 1), ("ResetEvent", 1)):
            h(n, npop)(lambda e: 1)
        @h("_beginthreadex")
        def _(e):
            e.log.append(f"_beginthreadex start={e.arg(2):#x} arg={e.arg(3):#x} (not started)")
            return new_handle(e)

        h("WaitForSingleObject", 2)(lambda e: 0)
        h("WaitForMultipleObjects", 4)(lambda e: 0)
        h("Sleep", 1)(lambda e: 0)
        for n in ("EnterCriticalSection", "LeaveCriticalSection", "InitializeCriticalSection", "DeleteCriticalSection"):
            h(n, 1)(lambda e: 0)
        h("InterlockedDecrement", 1)(lambda e: (e.w32(e.arg(0), e.r32(e.arg(0)) - 1), e.r32(e.arg(0)))[1])
        h("InterlockedExchange", 2)(lambda e: (lambda old: (e.w32(e.arg(0), e.arg(1)), old)[1])(e.r32(e.arg(0))))

        # ---- zlib
        @h("uncompress")
        def _(e):
            dst, dlen_p, src, slen = e.arg(0), e.arg(1), e.arg(2), e.arg(3)
            data = e.read(src, slen)
            cap = e.r32(dlen_p)
            try:
                out = zlib.decompress(data)
            except zlib.error as ex:
                e.log.append(f"uncompress failed: {ex}")
                return -3
            if len(out) > cap:
                return -5
            e.write(dst, out)
            e.w32(dlen_p, len(out))
            e.log.append(f"uncompress {slen} -> {len(out)} bytes")
            return 0

        # ---- MSVCP60 std::string (VC6 layout)
        @h(f"??0{S}QAE@ABV?$allocator@D@1@@Z", 1)
        def _(e):
            this = e.uc.reg_read(UC_X86_REG_ECX)
            e.str_init(this)
            return this

        @h(f"??0{S}QAE@PBDABV?$allocator@D@1@@Z", 2)
        def _(e):
            this = e.uc.reg_read(UC_X86_REG_ECX)
            e.str_init(this, e.cstr(e.arg(0)))
            return this

        @h(f"??0{S}QAE@ABV01@@Z", 1)
        def _(e):
            this = e.uc.reg_read(UC_X86_REG_ECX)
            e.str_init(this, e.str_get(e.arg(0)))
            return this

        @h(f"??1{S}QAE@XZ", 0)
        def _(e):
            return 0

        @h(f"?_Tidy@{S}AAEX_N@Z", 1)
        def _(e):
            this = e.uc.reg_read(UC_X86_REG_ECX)
            e.write(this + 4, b"\0" * 12)
            return 0

        @h(f"?c_str@{S}QBEPBDXZ", 0)
        def _(e):
            return e.r32(e.uc.reg_read(UC_X86_REG_ECX) + 4) or _empty(e)

        self.handlers[f"?data@{S}QBEPBDXZ"] = self.handlers[f"?c_str@{S}QBEPBDXZ"]
        h(f"?size@{S}QBEIXZ", 0)(lambda e: e.r32(e.uc.reg_read(UC_X86_REG_ECX) + 8))
        h(f"?length@{S}QBEIXZ", 0)(lambda e: e.r32(e.uc.reg_read(UC_X86_REG_ECX) + 8))
        h(f"?empty@{S}QBE_NXZ", 0)(lambda e: int(e.r32(e.uc.reg_read(UC_X86_REG_ECX) + 8) == 0))

        def assign(e, b):
            this = e.uc.reg_read(UC_X86_REG_ECX)
            e.str_set(this, b)
            return this

        h(f"??4{S}QAEAAV01@PBD@Z", 1)(lambda e: assign(e, e.cstr(e.arg(0))))
        h(f"??4{S}QAEAAV01@ABV01@@Z", 1)(lambda e: assign(e, e.str_get(e.arg(0))))
        h(f"?assign@{S}QAEAAV12@PBDI@Z", 2)(lambda e: assign(e, e.read(e.arg(0), e.arg(1)) if e.arg(1) else b""))
        h(f"?assign@{S}QAEAAV12@ABV12@II@Z", 3)(
            lambda e: assign(e, e.str_get(e.arg(0))[e.arg(1): e.arg(1) + e.arg(2) if e.arg(2) != 0xFFFFFFFF else None]))

        def append(e, b):
            this = e.uc.reg_read(UC_X86_REG_ECX)
            e.str_set(this, e.str_get(this) + b)
            return this

        h(f"?append@{S}QAEAAV12@PBD@Z", 1)(lambda e: append(e, e.cstr(e.arg(0))))
        h(f"?append@{S}QAEAAV12@PBDI@Z", 2)(lambda e: append(e, e.read(e.arg(0), e.arg(1)) if e.arg(1) else b""))
        h(f"?append@{S}QAEAAV12@ID@Z", 2)(lambda e: append(e, bytes([e.arg(1) & 0xFF]) * e.arg(0)))
        h(f"??Y{S}QAEAAV01@PBD@Z", 1)(lambda e: append(e, e.cstr(e.arg(0))))
        h(f"??Y{S}QAEAAV01@D@Z", 1)(lambda e: append(e, bytes([e.arg(0) & 0xFF])))
        h(f"??Y{S}QAEAAV01@ABV01@@Z", 1)(lambda e: append(e, e.str_get(e.arg(0))))

        # ---- MFC42 CString: one pointer to the chars, preceded by {nRefs, nDataLength, nAllocLength}
        def cs_get(e, obj):
            p = e.r32(obj)
            return e.read(p, e.r32(p - 8))

        def cs_set(e, obj, b, free_old=True):
            if free_old:
                e.free(e.r32(obj) - 12)
            blk = e.malloc(12 + len(b) + 1)
            e.write(blk, struct.pack("<iii", 1, len(b), len(b)) + b + b"\0")
            e.w32(obj, blk + 12)
            return obj

        ecx = lambda e: e.uc.reg_read(UC_X86_REG_ECX)  # noqa: E731
        # Only the ordinals the room module's config reader uses (QQTSection.dll 0x10035AE4).
        h("MFC42.DLL#537", 1)(lambda e: cs_set(e, ecx(e), e.cstr(e.arg(0)) if e.arg(0) else b"", False))  # CString(LPCSTR)
        h("MFC42.DLL#800", 0)(lambda e: e.free(e.r32(ecx(e)) - 12))                                        # ~CString()
        h("MFC42.DLL#858", 1)(lambda e: cs_set(e, ecx(e), cs_get(e, e.arg(0))))                            # operator=(const CString&)
        h("MFC42.DLL#940", 1)(lambda e: cs_set(e, ecx(e), cs_get(e, ecx(e)) + bytes([e.arg(0) & 0xFF])))   # operator+=(char)
        # AFXAPI (stdcall) operator+(const CString&, char) with a hidden return slot first
        h("MFC42.DLL#923", 3)(lambda e: cs_set(e, e.arg(0), cs_get(e, e.arg(1)) + bytes([e.arg(2) & 0xFF]), False))

        h("??0_Lockit@std@@QAE@XZ", 0)(lambda e: e.uc.reg_read(UC_X86_REG_ECX))
        h("??1_Lockit@std@@QAE@XZ", 0)(lambda e: 0)

        @h(f"??A{S}QAEAADI@Z", 1)
        def _(e):
            this = e.uc.reg_read(UC_X86_REG_ECX)
            return e.r32(this + 4) + e.arg(0)


def _empty(e: Win32Emu) -> int:
    if not hasattr(e, "_empty_str"):
        e._empty_str = e.malloc(4)
    return e._empty_str


def _ini_get(e: Win32Emu, fn: bytes, sec: bytes, key: bytes) -> bytes | None:
    name = fn.decode("latin-1").replace("\\", "/")
    p = pathlib.Path(name)
    if not p.is_absolute():
        p = e.root / name
    if not p.is_file():
        return None
    cur = None
    for line in p.read_bytes().splitlines():
        line = line.strip()
        if line.startswith(b"[") and b"]" in line:
            cur = line[1: line.index(b"]")].strip().lower()
        elif cur == sec.lower() and b"=" in line:
            k, v = line.split(b"=", 1)
            if k.strip().lower() == key.lower():
                return v.strip()
    return None


def _atoi(b: bytes) -> int:
    b = b.strip()
    n, i, neg = 0, 0, False
    if b[:1] in (b"-", b"+"):
        neg = b[:1] == b"-"
        i = 1
    while i < len(b) and 48 <= b[i] <= 57:
        n = n * 10 + b[i] - 48
        i += 1
    return -n if neg else n


def _scan(e: Win32Emu, s: bytes, fmt: bytes, argi: int) -> int:
    """sscanf subset: %d %u %x %s %c (with optional width), literal text and whitespace."""
    import re
    i = 0
    count = 0
    for m in re.finditer(rb"%(\*?)(\d*)(l|h)?([duxsc%]|\[\^?\]?[^\]]*\])|(\s+)|([^%\s]+)", fmt):
        skip, width, _, conv, ws, lit = m.groups()
        if conv and conv.startswith(b"["):
            mm = re.compile(rb"(" + conv + rb"+)").match(s, i, i + int(width) if width else len(s))
            if not mm:
                break
            i = mm.end()
            if not skip:
                e.write(e.arg(argi), mm.group() + b"\0")
                argi += 1
                count += 1
            continue
        if ws is not None:
            while i < len(s) and s[i] in b" \t\r\n":
                i += 1
            continue
        if lit is not None:
            if not s.startswith(lit, i):
                break
            i += len(lit)
            continue
        if conv == b"%":
            if s[i:i + 1] != b"%":
                break
            i += 1
            continue
        if conv != b"c":
            while i < len(s) and s[i] in b" \t\r\n":
                i += 1
        w = int(width) if width else None
        if conv == b"c":
            w = w or 1
            if i + w > len(s):
                break
            val, i = s[i:i + w], i + w
        elif conv == b"s":
            j = i
            while j < len(s) and s[j] not in b" \t\r\n" and (w is None or j - i < w):
                j += 1
            if j == i:
                break
            val, i = s[i:j] + b"\0", j
        else:
            pat = rb"[-+]?[0-9a-fA-F]+" if conv == b"x" else rb"[-+]?\d+"
            mm = re.compile(pat).match(s, i, i + w if w else len(s))
            if not mm:
                break
            val = int(mm.group(), 16 if conv == b"x" else 10)
            i = mm.end()
        if skip:
            continue
        dst = e.arg(argi)
        argi += 1
        if isinstance(val, bytes):
            e.write(dst, val)
        else:
            e.w32(dst, val)
        count += 1
    return count if count or i < len(s) else -1


def _format(e: Win32Emu, fmt: bytes, argi: int) -> bytes:
    """printf subset: %d %i %u %x %X %s %c %% with flags/width."""
    import re
    out = bytearray()
    pos = 0
    for m in re.finditer(rb"%([-+ 0#]*)(\d*)(?:\.(\d+))?(l|h|I64)?([diuxXscp%])", fmt):
        out += fmt[pos: m.start()]
        pos = m.end()
        flags, width, prec, _, conv = m.groups()
        if conv == b"%":
            out += b"%"
            continue
        v = e.arg(argi)
        argi += 1
        if conv in (b"d", b"i"):
            v = struct.unpack("<i", U32.pack(v))[0]
            s = str(v).encode()
        elif conv == b"u":
            s = str(v).encode()
        elif conv in (b"x", b"p"):
            s = f"{v:x}".encode()
        elif conv == b"X":
            s = f"{v:X}".encode()
        elif conv == b"c":
            s = bytes([v & 0xFF])
        else:
            s = e.cstr(v) if v else b"(null)"
            if prec:
                s = s[: int(prec)]
        w = int(width) if width else 0
        if len(s) < w:
            if b"-" in flags:
                s = s.ljust(w)
            elif b"0" in flags and conv != b"s":
                s = s.rjust(w, b"0")
            else:
                s = s.rjust(w)
        out += s
    out += fmt[pos:]
    return bytes(out)
