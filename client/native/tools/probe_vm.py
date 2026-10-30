"""Black-box run of one VM-protected Client.exe function in Unicorn.

Usage: probe_vm.py CLIENT_ROOT FUNC [--fill BYTE] [--state VTABLE]
Runs FUNC (thiscall) with ecx = a fake actor whose bytes are FILL (default 0)
and whose +0x3fc points to a zeroed fake scene. Reports writes to the actor,
the scene and globals, calls out of the VM into ordinary code, and memory the
function touched that was not set up (mapped as zero pages on demand).
"""
import pathlib, struct, sys, collections
sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
from win32emu import Win32Emu, STACK, STACK_SIZE, EmuStop
from unicorn import UC_HOOK_MEM_WRITE, UC_HOOK_MEM_READ, UC_HOOK_BLOCK, UC_PROT_ALL
from unicorn.x86_const import UC_X86_REG_EIP, UC_X86_REG_ECX, UC_X86_REG_ESP

root = pathlib.Path(sys.argv[1])
func = int(sys.argv[2], 16)
fill = int(sys.argv[sys.argv.index("--fill") + 1], 0) if "--fill" in sys.argv else 0

class Probe(Win32Emu):
    lazy: list = []
    def _on_bad_mem(self, uc, access, address, size, value, user):
        page = address & ~0xFFF
        try:
            uc.mem_map(page, 0x1000, UC_PROT_ALL)
        except Exception:
            return False
        self.lazy.append((uc.reg_read(UC_X86_REG_EIP), address, access))
        return True

emu = Probe(root / "Client.exe", root)
ACTOR_SIZE, SCENE_SIZE = 0x2000, 0x60000
actor = emu.malloc(ACTOR_SIZE)
emu.write(actor, bytes([fill]) * ACTOR_SIZE)
scene = emu.malloc(SCENE_SIZE)
emu.write(scene, bytes(SCENE_SIZE))
emu.w32(actor + 0x3FC, scene)
# State machine (ctor 0x005C06C9): vtable 0x7A2044, +4 current state, +0xC owner.
state_vtable = int(sys.argv[sys.argv.index("--state") + 1], 16) if "--state" in sys.argv else 0x7A21D0
state = emu.malloc(0x10)
emu.w32(state, state_vtable)
machine = emu.malloc(0x14)
emu.w32(machine, 0x7A2044)
emu.w32(machine + 4, state)
emu.w32(machine + 0xC, actor)
emu.w32(actor + 0x36C, machine)
# Harm-protection sub-object at +0x318 (setter 0x005B5202): +4 owner, +0x30
# the flash2.eff effect; a dummy effect skips loading the graphics.
emu.w32(actor + 0x318 + 4, actor)
emu.w32(actor + 0x318 + 0x30, 0x0BADEFFE)
uc = emu.uc
def module_file_name(e):
    path = b".\\Client.exe\0"
    n = min(len(path), e.arg(2))
    e.write(e.arg(1), path[:n])
    return n - 1
emu.handlers["GetModuleFileNameA"] = (module_file_name, 3)
emu.handlers["__dllonexit"] = (lambda e: e.arg(0), 0)   # cdecl: register a static's destructor; returns it
text = (0x401000, 0x401000 + 0x395430)
vm = (0x874000, 0x894000)

writes = collections.OrderedDict()
def on_write(uc, access, address, size, value, user):
    if STACK <= address < STACK + STACK_SIZE:
        return
    writes.setdefault((address, size), []).append((uc.reg_read(UC_X86_REG_EIP), value))
uc.hook_add(UC_HOOK_MEM_WRITE, on_write)

reads = collections.OrderedDict()
def on_read(uc, access, address, size, value, user):
    if actor <= address < actor + ACTOR_SIZE:
        reads.setdefault(address - actor, size)
uc.hook_add(UC_HOOK_MEM_READ, on_read, begin=actor, end=actor + ACTOR_SIZE - 1)

calls = []
prev = {"a": 0}
def on_block(uc, address, size, user):
    p = prev["a"]
    if text[0] <= address < text[1] and vm[0] <= p < vm[1]:
        esp = uc.reg_read(UC_X86_REG_ESP)
        args = [emu.r32(esp + 4 + 4 * i) for i in range(4)]
        calls.append((address, uc.reg_read(UC_X86_REG_ECX), emu.r32(esp), args))
    prev["a"] = address
uc.hook_add(UC_HOOK_BLOCK, on_block)

def where(a):
    if actor <= a < actor + ACTOR_SIZE: return f"actor+{a-actor:#x}"
    if scene <= a < scene + SCENE_SIZE: return f"scene+{a-scene:#x}"
    if machine <= a < machine + 0x14: return f"machine+{a-machine:#x}"
    if state <= a < state + 0x10: return f"state+{a-state:#x}"
    return f"{a:#x}"

try:
    rv = emu.call(func, ecx=actor, limit=20_000_000)
    print(f"returned eax={rv:#x}")
except EmuStop as e:
    print("stopped:", e)
print(f"actor={actor:#x} scene={scene:#x} fill={fill:#x}")
print("calls out of the VM (target, ecx, return, args):")
for t, ecx, ret, args in calls:
    print(f"  {t:#x} ecx={where(ecx)} ret={ret:#x} args={[where(a) for a in args]}")
print("writes (not stack):")
for (a, size), hits in writes.items():
    print(f"  {where(a)} size={size} values={[hex(v) for _, v in hits][:6]} eip={hits[0][0]:#x}")
print("actor fields read:", ", ".join(f"+{o:#x}/{s}" for o, s in sorted(reads.items())))
print("lazily mapped (eip, addr):", [(hex(e), where(a)) for e, a, _ in emu.lazy][:20])
