//go:build windows

package main

import (
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The JikuNode window is plain Win32 (user32 BUTTON and STATIC controls with
// the system message font and visual styles), so the tray needs no GUI
// toolkit and no cgo. It runs its own message loop on a locked OS thread;
// other goroutines talk to it only through PostMessage.

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modGdi32    = windows.NewLazySystemDLL("gdi32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW              = modUser32.NewProc("RegisterClassExW")
	procCreateWindowExW               = modUser32.NewProc("CreateWindowExW")
	procDefWindowProcW                = modUser32.NewProc("DefWindowProcW")
	procDestroyWindow                 = modUser32.NewProc("DestroyWindow")
	procShowWindow                    = modUser32.NewProc("ShowWindow")
	procIsIconic                      = modUser32.NewProc("IsIconic")
	procSetForegroundWindow           = modUser32.NewProc("SetForegroundWindow")
	procSetFocus                      = modUser32.NewProc("SetFocus")
	procGetFocus                      = modUser32.NewProc("GetFocus")
	procGetMessageW                   = modUser32.NewProc("GetMessageW")
	procTranslateMessage              = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW              = modUser32.NewProc("DispatchMessageW")
	procIsDialogMessageW              = modUser32.NewProc("IsDialogMessageW")
	procPostMessageW                  = modUser32.NewProc("PostMessageW")
	procSendMessageW                  = modUser32.NewProc("SendMessageW")
	procPostQuitMessage               = modUser32.NewProc("PostQuitMessage")
	procSetWindowTextW                = modUser32.NewProc("SetWindowTextW")
	procGetWindowTextW                = modUser32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW          = modUser32.NewProc("GetWindowTextLengthW")
	procSetWindowPos                  = modUser32.NewProc("SetWindowPos")
	procInvalidateRect                = modUser32.NewProc("InvalidateRect")
	procEnableWindow                  = modUser32.NewProc("EnableWindow")
	procLoadCursorW                   = modUser32.NewProc("LoadCursorW")
	procSystemParametersInfoW         = modUser32.NewProc("SystemParametersInfoW")
	procGetSystemMetrics              = modUser32.NewProc("GetSystemMetrics")
	procGetDpiForSystem               = modUser32.NewProc("GetDpiForSystem")
	procSetProcessDpiAwarenessContext = modUser32.NewProc("SetProcessDpiAwarenessContext")
	procAdjustWindowRectEx            = modUser32.NewProc("AdjustWindowRectEx")
	procGetSysColorBrush              = modUser32.NewProc("GetSysColorBrush")
	procGetSysColor                   = modUser32.NewProc("GetSysColor")
	procCreateIconFromResourceEx      = modUser32.NewProc("CreateIconFromResourceEx")
	procCreateFontIndirectW           = modGdi32.NewProc("CreateFontIndirectW")
	procSetBkColor                    = modGdi32.NewProc("SetBkColor")
	procSetTextColor                  = modGdi32.NewProc("SetTextColor")
	procCreatePopupMenu               = modUser32.NewProc("CreatePopupMenu")
	procAppendMenuW                   = modUser32.NewProc("AppendMenuW")
	procTrackPopupMenu                = modUser32.NewProc("TrackPopupMenu")
	procDestroyMenu                   = modUser32.NewProc("DestroyMenu")
	procGetWindowRect                 = modUser32.NewProc("GetWindowRect")
	procGetCursorPos                  = modUser32.NewProc("GetCursorPos")
	procCreateActCtxW                 = modKernel32.NewProc("CreateActCtxW")
	procActivateActCtx                = modKernel32.NewProc("ActivateActCtx")
	procGetModuleHandleW              = modKernel32.NewProc("GetModuleHandleW")
)

const (
	wsChild        = 0x40000000
	wsVisible      = 0x10000000
	wsCaption      = 0x00C00000
	wsSysMenu      = 0x00080000
	wsMinimizeBox  = 0x00020000
	wsTabStop      = 0x00010000
	wsExClientEdge = 0x00000200

	wsVScroll      = 0x00200000
	esMultiline    = 0x0004
	esUppercase    = 0x0008
	esNumber       = 0x2000
	esAutoVScroll  = 0x0040
	esAutoHScroll  = 0x0080
	esReadOnly     = 0x0800
	emSetSel       = 0x00B1
	emScrollCaret  = 0x00B7
	emLimitText    = 0x00C5
	emSetCueBanner = 0x1501

	bsPushButton  = 0x0
	bsCheckBox    = 0x2
	ssIcon        = 0x3
	ssEtchedHorz  = 0x10
	ssNoPrefix    = 0x80
	ssEndEllipsis = 0x4000

	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmSetFont     = 0x0030
	wmSetIcon     = 0x0080
	wmCommand     = 0x0111
	wmSysCommand  = 0x0112
	wmContextMenu = 0x007B
	scMinimize    = 0xF020

	mfString         = 0x0000
	tpmRightButton   = 0x0002
	tpmBottomAlign   = 0x0020
	tpmReturnCmd     = 0x0100
	wmCtlColorBtn    = 0x0135
	wmCtlColorStatic = 0x0138
	wmAppUpdate      = 0x8000 + 1
	wmAppShow        = 0x8000 + 2
	wmAppQuit        = 0x8000 + 3

	bmGetCheck = 0x00F0
	bmSetCheck = 0x00F1
	stmSetIcon = 0x0170
	bnClicked  = 0

	swHide       = 0
	swShowNormal = 1
	swShow       = 5
	swRestore    = 9

	colorWindow     = 5
	colorWindowText = 8
	colorGrayText   = 17

	smCxIcon   = 11
	smCxSmIcon = 49

	spiGetWorkArea         = 0x0030
	spiGetNonClientMetrics = 0x0029
	idcArrow               = 32512
	idOK                   = 1
	idCancel               = 2

	swpNoMove     = 0x0002
	swpNoZOrder   = 0x0004
	swpNoActivate = 0x0010
)

// errorTextColor is the red used for errors (a COLORREF: 0x00BBGGRR).
const errorTextColor = 0x001C2BC4

// Control ids.
const (
	idIcon = 100 + iota
	idTitle
	idDetail
	idInfo
	idCodeLabel
	idNameLabel
	idCode
	idName
	idBind
	idToggle
	idPause
	idSep1
	idSection
	idAutostart
	idConnect
	idSep2
	idPage // menu item: Open status page
	idLogs // menu item: Open log folder
	idHide
	idLogLabel
	idLog
	idPortLabel
	idPort
	idApplyPort
	idPath
	idMore
)

type wndClassEx struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     uintptr
	hIcon         uintptr
	hCursor       uintptr
	hbrBackground uintptr
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       uintptr
}

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      struct{ x, y int32 }
	private uint32
}

type winRect struct{ left, top, right, bottom int32 }

type logFont struct {
	height, width, escapement, orientation, weight int32
	italic, underline, strikeOut, charSet          byte
	outPrecision, clipPrecision, quality, pitch    byte
	faceName                                       [32]uint16
}

type nonClientMetrics struct {
	cbSize                                                              uint32
	borderWidth, scrollWidth, scrollHeight, captionWidth, captionHeight int32
	captionFont                                                         logFont
	smCaptionWidth, smCaptionHeight                                     int32
	smCaptionFont                                                       logFont
	menuWidth, menuHeight                                               int32
	menuFont, statusFont, messageFont                                   logFont
	paddedBorderWidth                                                   int32
}

type actCtx struct {
	cbSize                uint32
	dwFlags               uint32
	lpSource              *uint16
	processorArchitecture uint16
	langID                uint16
	lpAssemblyDirectory   *uint16
	lpResourceName        *uint16
	lpApplicationName     *uint16
	hModule               uintptr
}

// enableDPIAwareness makes the process system-DPI aware so the window and
// tray icon are drawn sharp instead of stretched. Call it before any window
// exists.
func enableDPIAwareness() {
	if procSetProcessDpiAwarenessContext.Find() == nil {
		const systemAware = ^uintptr(1) // DPI_AWARENESS_CONTEXT_SYSTEM_AWARE (-2)
		_, _, _ = procSetProcessDpiAwarenessContext.Call(systemAware)
	}
}

// commonControlsManifest selects version 6 of the common controls, which
// draws buttons and check boxes in the current Windows style.
const commonControlsManifest = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<assembly xmlns="urn:schemas-microsoft-com:asm.v1" manifestVersion="1.0">
<dependency><dependentAssembly><assemblyIdentity type="win32" name="Microsoft.Windows.Common-Controls" version="6.0.0.0" processorArchitecture="*" publicKeyToken="6595b64144ccf1df" language="*"/></dependentAssembly></dependency>
</assembly>
`

// activateVisualStyles activates, on the calling thread, an activation
// context with commonControlsManifest; controls created afterwards on this
// thread get the modern look. The executable needs no embedded manifest.
func activateVisualStyles() error {
	f, err := os.CreateTemp("", "jikunode-tray-*.manifest")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	_, err = f.WriteString(commonControlsManifest)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	ctx := actCtx{lpSource: windows.StringToUTF16Ptr(name)}
	ctx.cbSize = uint32(unsafe.Sizeof(ctx))
	h, _, err := procCreateActCtxW.Call(uintptr(unsafe.Pointer(&ctx)))
	if h == uintptr(windows.InvalidHandle) {
		return fmt.Errorf("create activation context: %w", err)
	}
	var cookie uintptr
	if r, _, err := procActivateActCtx.Call(h, uintptr(unsafe.Pointer(&cookie))); r == 0 {
		return fmt.Errorf("activate activation context: %w", err)
	}
	return nil
}

// windowActions are what the window's buttons do. They run on their own
// goroutine, never on the window thread.
type windowActions struct {
	toggleNode   func()
	pause        func()
	resume       func()
	openPage     func()
	openLogs     func()
	setAutostart func(bool)
	setConnect   func(bool)
	bind         func(code, name string)
	setPort      func(int)
	setPath      func(bool)
}

// windowState is everything the window shows.
type windowState struct {
	view      view
	wanted    bool // the node should run: the toggle button reads Stop
	busy      bool // a pause or resume is in flight
	nodeID    string
	version   string
	notice    string // a recent result or error, shown instead of the node line
	noticeErr bool   // notice is an error: shown in red
	autostart bool
	connect   bool
	bound     bool   // hides the binding code boxes
	binding   bool   // a bind is in flight
	log       string // the end of node.log, for the log box
	port      int    // the node's status port
	onPath    bool   // the program folder is on the user's PATH
}

type windowIcons struct{ status, small, big uintptr }

type window struct {
	title   string
	actions windowActions
	logger  *log.Logger

	// Owned by the window thread.
	hwnd      uintptr
	inst      uintptr
	dpi       int
	brush     uintptr
	font      uintptr
	bold      uintptr
	large     uintptr
	mono      uintptr
	ctrls     map[int]uintptr
	texts     map[int]string
	icons     map[iconState]windowIcons
	shown     windowState
	hasShown  bool
	bindShown bool
	infoErr   bool // the info line shows an error
	detailErr bool // the detail line shows an error

	mu      sync.Mutex
	pending windowState
}

// theWindow is the one JikuNode window; the window procedure reaches it here.
var theWindow *window

// newWindow creates the (hidden) JikuNode window, titled title, on its own
// thread.
func newWindow(title string, actions windowActions, icons map[iconState][]byte, logger *log.Logger) (*window, error) {
	w := &window{
		title:   title,
		actions: actions,
		logger:  logger,
		ctrls:   map[int]uintptr{},
		texts:   map[int]string{},
		icons:   map[iconState]windowIcons{},
	}
	ready := make(chan error, 1)
	go w.loop(icons, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return w, nil
}

// update shows s the next time the window thread runs.
func (w *window) update(s windowState) {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.pending = s
	w.mu.Unlock()
	w.post(wmAppUpdate)
}

// show opens the window and brings it to the front.
func (w *window) show() {
	if w != nil {
		w.post(wmAppShow)
	}
}

// destroy closes the window and ends its thread.
func (w *window) destroy() {
	if w != nil {
		w.post(wmAppQuit)
	}
}

func (w *window) post(msg uintptr) { _, _, _ = procPostMessageW.Call(w.hwnd, msg, 0, 0) }

func (w *window) loop(icons map[iconState][]byte, ready chan<- error) {
	runtime.LockOSThread() // the window belongs to this thread for good
	if err := activateVisualStyles(); err != nil {
		w.logger.Printf("window: visual styles: %v", err)
	}
	if err := w.create(icons); err != nil {
		ready <- err
		return
	}
	ready <- nil
	var m winMsg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		// Tab, Shift+Tab, Space and Esc work as in a dialog box.
		if r, _, _ := procIsDialogMessageW.Call(w.hwnd, uintptr(unsafe.Pointer(&m))); r != 0 {
			continue
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// s scales a layout value from 96-DPI pixels to the system DPI.
func (w *window) s(v int) int { return v * w.dpi / 96 }

// clientWidth and clientHeight are the window's inner size without the
// binding section, which adds bindHeight and moves what is below it down.
const clientWidth, clientHeight, bindHeight = 880, 368, 58

func (w *window) create(icons map[iconState][]byte) error {
	w.inst, _, _ = procGetModuleHandleW.Call(0)
	w.dpi = 96
	if procGetDpiForSystem.Find() == nil {
		if d, _, _ := procGetDpiForSystem.Call(); d > 0 {
			w.dpi = int(d)
		}
	}
	w.brush, _, _ = procGetSysColorBrush.Call(colorWindow)
	small, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	big, _, _ := procGetSystemMetrics.Call(smCxIcon)
	for state, ico := range icons {
		w.icons[state] = windowIcons{
			status: iconFromICO(ico, w.s(32)),
			small:  iconFromICO(ico, int(small)),
			big:    iconFromICO(ico, int(big)),
		}
	}

	className := windows.StringToUTF16Ptr("JikuNodeWindow")
	cursor, _, _ := procLoadCursorW.Call(0, idcArrow)
	wc := wndClassEx{
		lpfnWndProc:   windows.NewCallback(wndProc),
		hInstance:     w.inst,
		hIcon:         w.icons[iconOff].big,
		hIconSm:       w.icons[iconOff].small,
		hCursor:       cursor,
		hbrBackground: w.brush,
		lpszClassName: className,
	}
	wc.cbSize = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("register window class: %w", err)
	}

	const style = wsCaption | wsSysMenu | wsMinimizeBox
	rc := winRect{right: int32(w.s(clientWidth)), bottom: int32(w.s(clientHeight))}
	_, _, _ = procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&rc)), style, 0, 0)
	width, height := rc.right-rc.left, rc.bottom-rc.top
	var work winRect
	_, _, _ = procSystemParametersInfoW.Call(spiGetWorkArea, 0, uintptr(unsafe.Pointer(&work)), 0)
	x := work.left + (work.right-work.left-width)/2
	y := work.top + (work.bottom-work.top-height)/2

	theWindow = w
	hwnd, _, err := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(w.title))),
		style, uintptr(x), uintptr(y), uintptr(width), uintptr(height), 0, 0, w.inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("create window: %w", err)
	}
	w.hwnd = hwnd
	w.makeFonts()
	w.makeControls()
	w.layout(false)
	return nil
}

func (w *window) makeFonts() {
	var ncm nonClientMetrics
	ncm.cbSize = uint32(unsafe.Sizeof(ncm))
	_, _, _ = procSystemParametersInfoW.Call(spiGetNonClientMetrics, uintptr(ncm.cbSize), uintptr(unsafe.Pointer(&ncm)), 0)
	lf := ncm.messageFont
	if lf.faceName[0] == 0 {
		copy(lf.faceName[:], windows.StringToUTF16("Segoe UI"))
		lf.height = int32(-w.s(12))
	}
	newFont := func(f logFont) uintptr {
		h, _, _ := procCreateFontIndirectW.Call(uintptr(unsafe.Pointer(&f)))
		return h
	}
	w.font = newFont(lf)
	bold := lf
	bold.weight = 600
	w.bold = newFont(bold)
	large := bold
	large.height = lf.height * 3 / 2
	w.large = newFont(large)
	mono := lf
	mono.faceName = [32]uint16{}
	copy(mono.faceName[:], windows.StringToUTF16("Consolas"))
	mono.pitch = 0x31 // FIXED_PITCH | FF_MODERN
	w.mono = newFont(mono)
}

// Font choices for controls.
const (
	fontNormal = iota
	fontBold
	fontLarge
	fontMono
)

// control is one child window and where it goes (in 96-DPI pixels, with the
// binding section hidden).
type control struct {
	id             int
	class, text    string
	style, exStyle uintptr
	x, y, w, h     int
	font           int
	bind           bool // part of the binding section
	below          bool // moves down while the binding section shows
	grow           bool // grows taller while the binding section shows
}

// controls are created in this order, which is also the Tab order.
var controls = func() []control {
	const text = ssNoPrefix | ssEndEllipsis
	const edit = wsTabStop | esAutoHScroll
	c := []control{
		{id: idIcon, class: "STATIC", style: ssIcon, x: 20, y: 20, w: 32, h: 32},
		{id: idTitle, class: "STATIC", style: text, x: 64, y: 16, w: 356, h: 28, font: fontLarge},
		{id: idDetail, class: "STATIC", style: text, x: 64, y: 46, w: 356, h: 20},
		{id: idInfo, class: "STATIC", style: text, x: 64, y: 66, w: 356, h: 20},
		{id: idCodeLabel, class: "STATIC", text: "Binding code", style: ssNoPrefix, x: 64, y: 96, w: 140, h: 18, bind: true},
		{id: idNameLabel, class: "STATIC", text: "Name (optional)", style: ssNoPrefix, x: 212, y: 96, w: 128, h: 18, bind: true},
		{id: idCode, class: "EDIT", style: edit | esUppercase, exStyle: wsExClientEdge, x: 64, y: 116, w: 140, h: 26, bind: true},
		{id: idName, class: "EDIT", style: edit, exStyle: wsExClientEdge, x: 212, y: 116, w: 128, h: 26, bind: true},
		{id: idBind, class: "BUTTON", text: "Bind", style: bsPushButton | wsTabStop, x: 348, y: 115, w: 72, h: 28, bind: true},
		{id: idToggle, class: "BUTTON", text: "Stop node", style: bsPushButton | wsTabStop, x: 64, y: 98, w: 110, h: 30},
		{id: idPause, class: "BUTTON", text: "Pause", style: bsPushButton | wsTabStop, x: 182, y: 98, w: 110, h: 30},
		{id: idSep1, class: "STATIC", style: ssEtchedHorz, x: 20, y: 146, w: 400, h: 2},
		{id: idSection, class: "STATIC", text: "Preferences", style: ssNoPrefix, x: 20, y: 158, w: 400, h: 20, font: fontBold},
		{id: idAutostart, class: "BUTTON", text: "Start JikuNode when I sign in to Windows", style: bsCheckBox | wsTabStop, x: 20, y: 184, w: 400, h: 24},
		{id: idConnect, class: "BUTTON", text: "Connect to the hub automatically when JikuNode starts", style: bsCheckBox | wsTabStop, x: 20, y: 210, w: 400, h: 24},
		{id: idPath, class: "BUTTON", text: "Add jikunode to PATH (use it in a terminal)", style: bsCheckBox | wsTabStop, x: 20, y: 236, w: 400, h: 24},
		{id: idPortLabel, class: "STATIC", text: "Status port", style: ssNoPrefix, x: 20, y: 274, w: 90, h: 20},
		{id: idPort, class: "EDIT", style: edit | esNumber, exStyle: wsExClientEdge, x: 112, y: 269, w: 80, h: 26},
		{id: idApplyPort, class: "BUTTON", text: "Apply", style: bsPushButton | wsTabStop, x: 200, y: 268, w: 72, h: 28},
		{id: idSep2, class: "STATIC", style: ssEtchedHorz, x: 20, y: 310, w: 400, h: 2},
		{id: idMore, class: "BUTTON", text: "More ▾", style: bsPushButton | wsTabStop, x: 20, y: 324, w: 90, h: 30},
		{id: idHide, class: "BUTTON", text: "Minimize to tray", style: bsPushButton | wsTabStop, x: 280, y: 324, w: 140, h: 30},
		// The log box fills the right-hand column.
		{id: idLogLabel, class: "STATIC", text: "Node log", style: ssNoPrefix, x: 450, y: 16, w: 410, h: 20, font: fontBold},
		{id: idLog, class: "EDIT", style: wsTabStop | wsVScroll | esMultiline | esReadOnly | esAutoVScroll, exStyle: wsExClientEdge,
			x: 450, y: 40, w: 410, h: clientHeight - 40 - 14, font: fontMono, grow: true},
	}
	for i := range c {
		c[i].below = !c[i].bind && c[i].y > 90
	}
	return c
}()

func (w *window) makeControls() {
	fonts := map[int]uintptr{fontNormal: w.font, fontBold: w.bold, fontLarge: w.large, fontMono: w.mono}
	for _, c := range controls {
		h, _, _ := procCreateWindowExW.Call(c.exStyle,
			uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(c.class))),
			uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(c.text))),
			wsChild|wsVisible|c.style, 0, 0, 0, 0,
			w.hwnd, uintptr(c.id), w.inst, 0)
		_, _, _ = procSendMessageW.Call(h, wmSetFont, fonts[c.font], 1)
		w.ctrls[c.id] = h
		w.texts[c.id] = c.text
	}
	_, _, _ = procSendMessageW.Call(w.ctrls[idCode], emLimitText, 32, 0)
	_, _, _ = procSendMessageW.Call(w.ctrls[idName], emLimitText, 40, 0)
	_, _, _ = procSendMessageW.Call(w.ctrls[idLog], emLimitText, 0, 0) // no limit
	_, _, _ = procSendMessageW.Call(w.ctrls[idPort], emLimitText, 5, 0)
	_, _, _ = procSendMessageW.Call(w.ctrls[idCode], emSetCueBanner, 1, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("XXXXX-XXXXX"))))
	_, _, _ = procSendMessageW.Call(w.ctrls[idName], emSetCueBanner, 1, uintptr(unsafe.Pointer(windows.StringToUTF16Ptr("e.g. office-pc"))))
}

// layout places the controls and sizes the window, with or without the
// binding section.
func (w *window) layout(showBind bool) {
	w.bindShown = showBind
	shift := 0
	if showBind {
		shift = bindHeight
	}
	for _, c := range controls {
		h := w.ctrls[c.id]
		y, ht := c.y, c.h
		if c.below {
			y += shift
		}
		if c.grow {
			ht += shift
		}
		_, _, _ = procSetWindowPos.Call(h, 0, uintptr(w.s(c.x)), uintptr(w.s(y)), uintptr(w.s(c.w)), uintptr(w.s(ht)), swpNoZOrder|swpNoActivate)
		if c.bind {
			cmd := uintptr(swHide)
			if showBind {
				cmd = swShow
			}
			_, _, _ = procShowWindow.Call(h, cmd)
		}
	}
	const style = wsCaption | wsSysMenu | wsMinimizeBox
	rc := winRect{right: int32(w.s(clientWidth)), bottom: int32(w.s(clientHeight + shift))}
	_, _, _ = procAdjustWindowRectEx.Call(uintptr(unsafe.Pointer(&rc)), style, 0, 0)
	_, _, _ = procSetWindowPos.Call(w.hwnd, 0, 0, 0, uintptr(rc.right-rc.left), uintptr(rc.bottom-rc.top), swpNoMove|swpNoZOrder|swpNoActivate)
}

// text returns a control's text, trimmed.
func (w *window) text(id int) string {
	n, _, _ := procGetWindowTextLengthW.Call(w.ctrls[id])
	buf := make([]uint16, n+1)
	_, _, _ = procGetWindowTextW.Call(w.ctrls[id], uintptr(unsafe.Pointer(&buf[0])), n+1)
	return strings.TrimSpace(windows.UTF16ToString(buf))
}

func wndProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	w := theWindow
	if w != nil {
		switch msg {
		case wmCtlColorStatic, wmCtlColorBtn:
			bg, _, _ := procGetSysColor.Call(colorWindow)
			fg, _, _ := procGetSysColor.Call(colorWindowText)
			if lParam == w.ctrls[idDetail] && w.detailErr {
				fg = errorTextColor
			}
			if lParam == w.ctrls[idInfo] {
				if w.infoErr {
					fg = errorTextColor
				} else {
					fg, _, _ = procGetSysColor.Call(colorGrayText)
				}
			}
			_, _, _ = procSetBkColor.Call(wParam, bg)
			_, _, _ = procSetTextColor.Call(wParam, fg)
			return w.brush
		case wmCommand:
			if wParam>>16&0xffff == bnClicked {
				w.command(int(wParam & 0xffff))
			}
			return 0
		case wmContextMenu:
			// Right-click on the window (not in a text box) opens the menu
			// at the pointer; Shift+F10 or the menu key gives -1,-1.
			x, y := int32(int16(lParam&0xffff)), int32(int16(lParam>>16&0xffff))
			if x == -1 && y == -1 {
				var pt struct{ x, y int32 }
				_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
				x, y = pt.x, pt.y
			}
			w.menu(x, y, false)
			return 0
		case wmSysCommand:
			// The title bar's minimize button sends JikuNode to the tray
			// instead of the taskbar.
			if wParam&0xfff0 == scMinimize {
				_, _, _ = procShowWindow.Call(hwnd, swHide)
				return 0
			}
		case wmClose:
			// Closing the window leaves JikuNode running in the tray.
			_, _, _ = procShowWindow.Call(hwnd, swHide)
			return 0
		case wmAppUpdate:
			w.apply()
			return 0
		case wmAppShow:
			w.present()
			return 0
		case wmAppQuit:
			_, _, _ = procDestroyWindow.Call(hwnd)
			return 0
		case wmDestroy:
			_, _, _ = procPostQuitMessage.Call(0)
			return 0
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

// command handles a button click on the window thread.
func (w *window) command(id int) {
	a := w.actions
	switch id {
	case idToggle:
		w.enable(idToggle, false)
		go a.toggleNode()
	case idPause:
		w.enable(idPause, false)
		if w.shown.view.canResume {
			go a.resume()
		} else {
			go a.pause()
		}
	case idAutostart, idConnect, idPath:
		on := !w.checked(id)
		w.setCheck(id, on) // at once; the next update confirms it
		switch id {
		case idAutostart:
			go a.setAutostart(on)
		case idConnect:
			go a.setConnect(on)
		case idPath:
			go a.setPath(on)
		}
	case idApplyPort:
		w.applyPort()
	case idBind, idOK:
		if id == idOK {
			if f, _, _ := procGetFocus.Call(); f == w.ctrls[idPort] {
				w.applyPort()
				return
			}
			if !w.bindShown {
				return
			}
		}
		code, name := w.text(idCode), w.text(idName)
		if code == "" {
			_, _, _ = procSetFocus.Call(w.ctrls[idCode])
			return
		}
		w.enable(idBind, false)
		w.setText(idBind, "Binding…")
		go a.bind(code, name)
	case idMore:
		var r winRect
		_, _, _ = procGetWindowRect.Call(w.ctrls[idMore], uintptr(unsafe.Pointer(&r)))
		w.menu(r.left, r.top, true)
	case idPage:
		go a.openPage()
	case idLogs:
		go a.openLogs()
	case idHide, idCancel:
		_, _, _ = procShowWindow.Call(w.hwnd, swHide)
	}
}

// menu shows the window's context menu at screen point x,y (above it when
// above is set, as for the More button at the bottom) and runs the chosen
// item.
func (w *window) menu(x, y int32, above bool) {
	m, _, _ := procCreatePopupMenu.Call()
	if m == 0 {
		return
	}
	defer procDestroyMenu.Call(m)
	for _, it := range []struct {
		id   int
		text string
	}{{idPage, "Open status page"}, {idLogs, "Open log folder"}} {
		_, _, _ = procAppendMenuW.Call(m, mfString, uintptr(it.id), uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(it.text))))
	}
	flags := uintptr(tpmReturnCmd | tpmRightButton)
	if above {
		flags |= tpmBottomAlign
	}
	id, _, _ := procTrackPopupMenu.Call(m, flags, uintptr(x), uintptr(y), 0, w.hwnd, 0)
	if id != 0 {
		w.command(int(id))
	}
}

// applyPort sends the port typed in the window to the app.
func (w *window) applyPort() {
	p, err := strconv.Atoi(w.text(idPort))
	if err != nil {
		p = 0 // setPort reports it
	}
	go w.actions.setPort(p)
}

func (w *window) present() {
	if r, _, _ := procIsIconic.Call(w.hwnd); r != 0 {
		_, _, _ = procShowWindow.Call(w.hwnd, swRestore)
	} else {
		_, _, _ = procShowWindow.Call(w.hwnd, swShowNormal)
	}
	_, _, _ = procSetForegroundWindow.Call(w.hwnd)
	if w.bindShown {
		_, _, _ = procSetFocus.Call(w.ctrls[idCode])
	} else {
		_, _, _ = procSetFocus.Call(w.ctrls[idToggle])
	}
}

// apply shows the pending state, touching only what changed.
func (w *window) apply() {
	w.mu.Lock()
	s := w.pending
	w.mu.Unlock()
	first := !w.hasShown
	w.hasShown = true

	if first || s.view.icon != w.shown.view.icon {
		ic := w.icons[s.view.icon]
		_, _, _ = procSendMessageW.Call(w.ctrls[idIcon], stmSetIcon, ic.status, 0)
		_, _, _ = procSendMessageW.Call(w.hwnd, wmSetIcon, 0, ic.small)
		_, _, _ = procSendMessageW.Call(w.hwnd, wmSetIcon, 1, ic.big)
	}
	w.setText(idTitle, capitalize(strings.TrimPrefix(s.view.title, "JikuNode: ")))
	if errNow := s.view.icon == iconError; errNow != w.detailErr {
		w.detailErr = errNow
		_, _, _ = procInvalidateRect.Call(w.ctrls[idDetail], 0, 1)
	}
	w.setText(idDetail, s.view.detail)
	info := s.notice
	if info == "" && s.nodeID != "" {
		info = "Node " + s.nodeID
		if s.version != "" {
			info += "  ·  version " + s.version
		}
	}
	if errNow := s.notice != "" && s.noticeErr; errNow != w.infoErr {
		w.infoErr = errNow
		_, _, _ = procInvalidateRect.Call(w.ctrls[idInfo], 0, 1)
	}
	w.setText(idInfo, info)

	if s.wanted {
		w.setText(idToggle, "Stop node")
	} else {
		w.setText(idToggle, "Start node")
	}
	w.enable(idToggle, true)
	if s.view.canResume {
		w.setText(idPause, "Resume")
	} else {
		w.setText(idPause, "Pause")
	}
	w.enable(idPause, (s.view.canPause || s.view.canResume) && !s.busy)
	w.setCheck(idAutostart, s.autostart)
	w.setCheck(idConnect, s.connect)
	w.setCheck(idPath, s.onPath)
	if first || s.port != w.shown.port {
		w.setText(idPort, strconv.Itoa(s.port))
	}
	if first || s.bound == w.bindShown {
		w.layout(!s.bound)
		if s.bound {
			w.setText(idCode, "")
			w.setText(idName, "")
		}
	}
	if s.binding {
		w.setText(idBind, "Binding…")
	} else {
		w.setText(idBind, "Bind")
	}
	w.enable(idBind, !s.binding)
	if s.log != w.texts[idLog] {
		w.setText(idLog, s.log)
		// Show the newest lines.
		n := uintptr(len(s.log) + 1)
		_, _, _ = procSendMessageW.Call(w.ctrls[idLog], emSetSel, n, n)
		_, _, _ = procSendMessageW.Call(w.ctrls[idLog], emScrollCaret, 0, 0)
	}
	w.shown = s
}

func (w *window) setText(id int, text string) {
	if w.texts[id] == text {
		return
	}
	w.texts[id] = text
	_, _, _ = procSetWindowTextW.Call(w.ctrls[id], uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(text))))
}

func (w *window) enable(id int, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	_, _, _ = procEnableWindow.Call(w.ctrls[id], v)
}

func (w *window) checked(id int) bool {
	r, _, _ := procSendMessageW.Call(w.ctrls[id], bmGetCheck, 0, 0)
	return r == 1
}

func (w *window) setCheck(id int, on bool) {
	v := uintptr(0)
	if on {
		v = 1
	}
	_, _, _ = procSendMessageW.Call(w.ctrls[id], bmSetCheck, v, 0)
}

func capitalize(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if n == 0 {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// iconFromICO makes an icon handle of size×size pixels from a .ico file,
// using the smallest frame at least that large (or the largest frame).
func iconFromICO(ico []byte, size int) uintptr {
	if len(ico) < 6 {
		return 0
	}
	count := int(binary.LittleEndian.Uint16(ico[4:]))
	best, bestW := -1, 0
	for i := 0; i < count && 6+16*i+16 <= len(ico); i++ {
		wd := int(ico[6+16*i])
		if wd == 0 {
			wd = 256
		}
		if best < 0 || (bestW < size && wd > bestW) || (wd >= size && wd < bestW) {
			best, bestW = i, wd
		}
	}
	if best < 0 {
		return 0
	}
	e := ico[6+16*best:]
	n := binary.LittleEndian.Uint32(e[8:])
	off := binary.LittleEndian.Uint32(e[12:])
	if uint64(off)+uint64(n) > uint64(len(ico)) || n == 0 {
		return 0
	}
	const iconVersion = 0x00030000
	h, _, _ := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&ico[off])), uintptr(n), 1, iconVersion, uintptr(size), uintptr(size), 0)
	return h
}
