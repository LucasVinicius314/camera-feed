//go:build windows

package main

import (
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetModuleHandle = kernel32.NewProc("GetModuleHandleW")

	user32               = syscall.NewLazyDLL("user32.dll")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procLoadCursorW      = user32.NewProc("LoadCursorW")
	procPostQuitMessage  = user32.NewProc("PostQuitMessage")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procSetWindowTextW   = user32.NewProc("SetWindowTextW")
	procShowWindow       = user32.NewProc("ShowWindow")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procBeginPaint       = user32.NewProc("BeginPaint")
	procEndPaint         = user32.NewProc("EndPaint")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")

	gdi32             = syscall.NewLazyDLL("gdi32.dll")
	procStretchDIBits = gdi32.NewProc("StretchDIBits")
	procPatBlt        = gdi32.NewProc("PatBlt")
)

const (
	wsOverlappedWindow = 0x00CF0000

	swShow = 5

	wmDestroy = 0x0002
	wmPaint   = 0x000F
	wmClose   = 0x0010
	wmKeydown = 0x0100

	vkT = 0x54

	hwndTopmost   = ^uintptr(0)
	hwndNotopmost = ^uintptr(1)

	swpNomove     = 0x0002
	swpNosize     = 0x0001
	swpNoactivate = 0x0010

	dibRgbColors = 0
	srccopy      = 0x00CC0020
	blackness    = 0x00000042
)

type wndclassex struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       windows.Handle
}

type msg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
}

type rect struct {
	left, top, right, bottom int32
}

type paintstruct struct {
	hdc         uintptr
	fErase      int32
	rcPaint     rect
	fRestore    int32
	fIncUpdate  int32
	rgbReserved [32]byte
}

type bitmapinfoheader struct {
	biSize          uint32
	biWidth         int32
	biHeight        int32
	biPlanes        uint16
	biBitCount      uint16
	biCompression   uint32
	biSizeImage     uint32
	biXPelsPerMeter int32
	biYPelsPerMeter int32
	biClrUsed       uint32
	biClrImportant  uint32
}

type bitmapinfo struct {
	bmiHeader bitmapinfoheader
	bmiColors [1]uint32
}

var (
	frameMu     sync.Mutex
	framePixels []byte
	frameWidth  int
	frameHeight int
	parentHWND  uintptr
	alwaysOnTop bool
	decoderCmd  *exec.Cmd
	decoderMu   sync.Mutex
)

func runWindow(onReady func()) {
	hInstance, _, _ := procGetModuleHandle.Call(0)
	className, _ := windows.UTF16PtrFromString("SauronWindow")
	cursor, _, _ := procLoadCursorW.Call(0, 32512)

	wc := wndclassex{
		cbSize:        uint32(unsafe.Sizeof(wndclassex{})),
		lpfnWndProc:   syscall.NewCallback(wndProc),
		hInstance:     windows.Handle(hInstance),
		hCursor:       windows.Handle(cursor),
		hbrBackground: windows.Handle(0),
		lpszClassName: className,
	}
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))

	title, _ := windows.UTF16PtrFromString("Sauron — waiting for stream")
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow,
		100, 100, 1280, 720,
		0, 0,
		uintptr(hInstance),
		0,
	)
	if hwnd == 0 {
		log.Fatal("CreateWindowEx failed")
	}
	parentHWND = hwnd
	procShowWindow.Call(hwnd, swShow)

	go onReady()

	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if r == 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd, message, wParam, lParam uintptr) uintptr {
	switch message {
	case wmPaint:
		var ps paintstruct
		procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		paintFrame(hwnd, ps.hdc)
		procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
		return 0

	case wmKeydown:
		if wParam == vkT {
			toggleAlwaysOnTop(hwnd)
		}

	case wmClose:
		decoderMu.Lock()
		if decoderCmd != nil {
			_ = decoderCmd.Process.Kill()
		}
		decoderMu.Unlock()
		procPostQuitMessage.Call(0)
		return 0

	case wmDestroy:
		procPostQuitMessage.Call(0)
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

func paintFrame(hwnd, hdc uintptr) {
	var cr rect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&cr)))
	cw := int(cr.right - cr.left)
	ch := int(cr.bottom - cr.top)

	frameMu.Lock()
	pixels := framePixels
	fw := frameWidth
	fh := frameHeight
	frameMu.Unlock()

	if len(pixels) == 0 || fw == 0 || fh == 0 {
		procPatBlt.Call(hdc, 0, 0, uintptr(cw), uintptr(ch), blackness)
		return
	}

	bmi := bitmapinfo{
		bmiHeader: bitmapinfoheader{
			biSize:     uint32(unsafe.Sizeof(bitmapinfoheader{})),
			biWidth:    int32(fw),
			biHeight:   -int32(fh), // negative = top-down
			biPlanes:   1,
			biBitCount: 24,
		},
	}
	procStretchDIBits.Call(
		hdc,
		0, 0, uintptr(cw), uintptr(ch),
		0, 0, uintptr(fw), uintptr(fh),
		uintptr(unsafe.Pointer(&pixels[0])),
		uintptr(unsafe.Pointer(&bmi)),
		dibRgbColors,
		srccopy,
	)
}

func setFrame(pixels []byte, w, h int) {
	frameMu.Lock()
	framePixels = pixels
	frameWidth = w
	frameHeight = h
	frameMu.Unlock()

	if parentHWND == 0 || len(pixels) == 0 {
		procInvalidateRect.Call(parentHWND, 0, 1)
		return
	}

	hdc, _, _ := procGetDC.Call(parentHWND)
	if hdc == 0 {
		return
	}
	paintFrame(parentHWND, hdc)
	procReleaseDC.Call(parentHWND, hdc)
}

func toggleAlwaysOnTop(hwnd uintptr) {
	alwaysOnTop = !alwaysOnTop
	insertAfter := hwndNotopmost
	if alwaysOnTop {
		insertAfter = hwndTopmost
	}
	procSetWindowPos.Call(hwnd, insertAfter, 0, 0, 0, 0, swpNomove|swpNosize|swpNoactivate)

	title := "Sauron"
	if alwaysOnTop {
		title = "Sauron [on top]"
	}
	t, _ := windows.UTF16PtrFromString(title)
	procSetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(t)))
}

var reDim = regexp.MustCompile(`\b(\d+)x(\d+)\b`)

func startDecoder() {
	decoderMu.Lock()
	if decoderCmd != nil {
		decoderMu.Unlock()
		return
	}

	cmd := exec.Command("ffmpeg",
		"-fflags", "nobuffer",
		"-flags", "low_delay",
		"-i", streamURL,
		"-f", "rawvideo",
		"-pix_fmt", "bgr24",
		"-vcodec", "rawvideo",
		"-an",
		"-sn",
		"pipe:1",
	)
	decoderCmd = cmd
	decoderMu.Unlock()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("decoder stdout pipe: %v", err)
		return
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("decoder stderr pipe: %v", err)
		return
	}
	if err := cmd.Start(); err != nil {
		log.Printf("decoder start: %v", err)
		return
	}
	log.Println("decoder started")

	dimCh := make(chan [2]int, 1)
	go parseStderr(stderrPipe, dimCh)

	dim, ok := <-dimCh
	if !ok || dim[0] == 0 {
		log.Println("could not determine stream dimensions")
		_ = cmd.Process.Kill()
		return
	}
	w, h := dim[0], dim[1]
	log.Printf("stream size: %dx%d", w, h)

	t, _ := windows.UTF16PtrFromString("Sauron")
	procSetWindowTextW.Call(parentHWND, uintptr(unsafe.Pointer(t)))

	frameSize := w * h * 3
	buf := make([]byte, frameSize)
	for {
		_, err := io.ReadFull(stdout, buf)
		if err != nil {
			log.Printf("decoder read: %v", err)
			break
		}
		frame := make([]byte, frameSize)
		copy(frame, buf)
		setFrame(frame, w, h)
	}

	_ = cmd.Wait()
	log.Println("decoder exited")
	decoderMu.Lock()
	decoderCmd = nil
	decoderMu.Unlock()

	setFrame(nil, 0, 0)
	t2, _ := windows.UTF16PtrFromString("Sauron — waiting for stream")
	procSetWindowTextW.Call(parentHWND, uintptr(unsafe.Pointer(t2)))
}

func parseStderr(r io.Reader, dimCh chan<- [2]int) {
	sent := false
	buf := make([]byte, 4096)
	var accum []byte

	for {
		n, err := r.Read(buf)
		if n > 0 {
			accum = append(accum, buf[:n]...)
			for {
				idx := -1
				for i, b := range accum {
					if b == '\n' || b == '\r' {
						idx = i
						break
					}
				}
				if idx < 0 {
					break
				}
				line := string(accum[:idx])
				accum = accum[idx+1:]
				if line != "" && !strings.HasPrefix(strings.TrimSpace(line), "frame=") {
					log.Printf("[ffmpeg] %s", line)
				}
				if !sent && len(line) > 0 {
					if m := reDim.FindStringSubmatch(line); m != nil {
						var w, h int
						fmt.Sscanf(m[1], "%d", &w)
						fmt.Sscanf(m[2], "%d", &h)
						if w >= 64 && h >= 64 {
							dimCh <- [2]int{w, h}
							sent = true
						}
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	if !sent {
		close(dimCh)
	}
}
