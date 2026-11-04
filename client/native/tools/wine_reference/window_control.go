//go:build windows

// Window-message input for the isolated original-client reference. Capture
// uses macOS screencapture; GDI/PrintWindow do not capture its GPU surface.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var user32 = syscall.NewLazyDLL("user32.dll")

type rect struct{ Left, Top, Right, Bottom int32 }
type window struct {
	HWND    uintptr `json:"hwnd"`
	PID     uint32  `json:"pid"`
	Title   string  `json:"title"`
	Class   string  `json:"class"`
	Rect    rect    `json:"rect"`
	Visible bool    `json:"visible"`
}

func api(name string) *syscall.LazyProc { return user32.NewProc(name) }

func windows(pid uint32) []window {
	ws := []window{}
	callback := syscall.NewCallback(func(hwnd, _ uintptr) uintptr {
		var owner uint32
		api("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if pid != 0 && pid != owner {
			return 1
		}
		title, class := make([]uint16, 1024), make([]uint16, 256)
		api("GetWindowTextW").Call(hwnd, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
		api("GetClassNameW").Call(hwnd, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
		var bounds rect
		api("GetClientRect").Call(hwnd, uintptr(unsafe.Pointer(&bounds)))
		visible, _, _ := api("IsWindowVisible").Call(hwnd)
		ws = append(ws, window{hwnd, owner, syscall.UTF16ToString(title), syscall.UTF16ToString(class), bounds, visible != 0})
		return 1
	})
	api("EnumWindows").Call(callback, 0)
	return ws
}

func post(hwnd uintptr, message uint32, wparam, lparam uintptr) {
	ok, _, err := api("PostMessageW").Call(hwnd, uintptr(message), wparam, lparam)
	if ok == 0 {
		panic(fmt.Errorf("PostMessageW: %w", err))
	}
}

func main() {
	pid := flag.Uint("pid", 0, "restrict to this Windows PID")
	handle := flag.String("hwnd", "0", "target window handle")
	click := flag.String("click", "", "client-coordinate x,y")
	activate := flag.Bool("activate", false, "foreground the window and move the host cursor")
	text := flag.Bool("text-stdin", false, "send stdin as WM_CHAR without logging it")
	key := flag.Uint("key", 0, "virtual key")
	count := flag.Int("count", 1, "key repetitions")
	closeWindow := flag.Bool("close", false, "post WM_CLOSE")
	terminate := flag.Bool("terminate", false, "end the exact reference executable after window close")
	executable := flag.String("executable", "", "required exact executable path for --terminate")
	system := flag.Bool("system", false, "report Windows code pages")
	flag.Parse()
	if *system {
		k := syscall.NewLazyDLL("kernel32.dll")
		a, _, _ := k.NewProc("GetACP").Call()
		o, _, _ := k.NewProc("GetOEMCP").Call()
		l, _, _ := k.NewProc("GetSystemDefaultLCID").Call()
		json.NewEncoder(os.Stdout).Encode(map[string]uintptr{"acp": a, "oemcp": o, "lcid": l})
		return
	}
	h, err := strconv.ParseUint(*handle, 0, 32)
	if err != nil {
		panic(err)
	}
	if h == 0 {
		json.NewEncoder(os.Stdout).Encode(windows(uint32(*pid)))
		return
	}
	hwnd := uintptr(h)
	var owner uint32
	api("GetWindowThreadProcessId").Call(hwnd, uintptr(unsafe.Pointer(&owner)))
	if *pid == 0 || owner != uint32(*pid) {
		panic("input requires --pid matching the target window")
	}
	if *terminate {
		if *executable == "" {
			panic("--terminate requires --executable")
		}
		kernel := syscall.NewLazyDLL("kernel32.dll")
		process, _, err := kernel.NewProc("OpenProcess").Call(0x1001, 0, uintptr(*pid))
		if process == 0 {
			panic(err)
		}
		defer kernel.NewProc("CloseHandle").Call(process)
		path := make([]uint16, 4096)
		size := uint32(len(path))
		ok, _, err := kernel.NewProc("QueryFullProcessImageNameW").Call(process, 0, uintptr(unsafe.Pointer(&path[0])), uintptr(unsafe.Pointer(&size)))
		if ok == 0 {
			panic(err)
		}
		if !strings.EqualFold(syscall.UTF16ToString(path), *executable) {
			panic("reference executable path does not match; process left running")
		}
		ok, _, err = kernel.NewProc("TerminateProcess").Call(process, 0)
		if ok == 0 {
			panic(err)
		}
		return
	}
	if *activate {
		api("SetForegroundWindow").Call(hwnd)
	}
	if *click != "" {
		var x, y int32
		if _, err := fmt.Sscanf(*click, "%d,%d", &x, &y); err != nil {
			panic(err)
		}
		if *activate {
			point := struct{ X, Y int32 }{x, y}
			api("ClientToScreen").Call(hwnd, uintptr(unsafe.Pointer(&point)))
			api("SetCursorPos").Call(uintptr(point.X), uintptr(point.Y))
		}
		pos := uintptr(uint32(uint16(y))<<16 | uint32(uint16(x)))
		post(hwnd, 0x200, 0, pos)
		post(hwnd, 0x201, 1, pos)
		time.Sleep(50 * time.Millisecond)
		post(hwnd, 0x202, 0, pos)
	}
	if *text {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			panic(err)
		}
		for _, c := range utf16.Encode([]rune(strings.TrimSuffix(string(data), "\n"))) {
			post(hwnd, 0x102, uintptr(c), 1)
			time.Sleep(15 * time.Millisecond)
		}
	}
	for i := 0; *key != 0 && i < *count; i++ {
		post(hwnd, 0x100, uintptr(*key), 1)
		time.Sleep(50 * time.Millisecond)
		post(hwnd, 0x101, uintptr(*key), 0xC0000001)
	}
	if *closeWindow {
		post(hwnd, 0x10, 0, 0)
	}
}
