//go:build windows

package winapp

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	// AppDirName is the directory name used under every Windows root.
	AppDirName = "jikunode"
	// NodeExe and TrayExe are the installed program file names.
	NodeExe = "jikunode.exe"
	TrayExe = "jikunode-tray.exe"

	// runKeyPath is the per-user autostart key; RunValueName is our value.
	runKeyPath   = `Software\Microsoft\Windows\CurrentVersion\Run`
	RunValueName = "JikuNode"

	// The names are in the session-local namespace, so they only reach the
	// tray app of the same logon session.
	trayMutexName    = `Local\JikuNodeTray`
	quitEventName    = `Local\JikuNodeTrayQuit`
	restartEventName = `Local\JikuNodeTrayRestart`
	showEventName    = `Local\JikuNodeTrayShow`
)

// localAppData returns %LOCALAPPDATA%.
func localAppData() (string, error) {
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return dir, nil
	}
	return os.UserCacheDir()
}

// InstallDir is where the per-user installer puts both executables:
// %LOCALAPPDATA%\Programs\jikunode (no administrator rights needed).
func InstallDir() (string, error) {
	root, err := localAppData()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Programs", AppDirName), nil
}

// PortableMarker is the file that, next to jikunode.exe, makes JikuNode
// portable: config, key, logs and preferences then live in the data folder
// beside the programs instead of under the user's profile.
const PortableMarker = "jikunode.portable"

// PortableDataDir returns the data folder of a portable copy (the folder
// "data" next to the running program) and whether this copy is portable.
func PortableDataDir() (string, bool) {
	exe, err := os.Executable()
	if err != nil {
		return "", false
	}
	dir := filepath.Dir(exe)
	if info, err := os.Stat(filepath.Join(dir, PortableMarker)); err != nil || info.IsDir() {
		return "", false
	}
	return filepath.Join(dir, "data"), true
}

// ConfigRoot is the root for configuration and the node key:
// $XDG_CONFIG_HOME when set (for parity with Unix), data\config in a
// portable copy, otherwise %APPDATA%.
func ConfigRoot() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return xdg, nil
	}
	if data, ok := PortableDataDir(); ok {
		return filepath.Join(data, "config"), nil
	}
	return os.UserConfigDir()
}

// StateRoot is the root for local, non-roaming state: $XDG_STATE_HOME when
// set, data\state in a portable copy, otherwise %LOCALAPPDATA%.
func StateRoot() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
		return xdg, nil
	}
	if data, ok := PortableDataDir(); ok {
		return filepath.Join(data, "state"), nil
	}
	return localAppData()
}

// StartMenuDir is the user's Start menu Programs folder, where the installer
// puts the JikuNode shortcut.
func StartMenuDir() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
}

// LogDir is where the tray app writes the node's log files.
func LogDir() (string, error) {
	root, err := StateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, AppDirName, "logs"), nil
}

// ---- autostart --------------------------------------------------------------

// SetAutostart makes the tray app at trayPath start, with args, when the user
// logs on. Arguments must not contain quotes or spaces.
func SetAutostart(trayPath string, args ...string) error {
	for _, a := range args {
		if strings.ContainsAny(a, "\" 	") {
			return fmt.Errorf("invalid autostart argument %q", a)
		}
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open autostart key: %w", err)
	}
	defer k.Close()
	return k.SetStringValue(RunValueName, strings.TrimSpace(`"`+trayPath+`" `+strings.Join(args, " ")))
}

// RemoveAutostart deletes the autostart entry and reports whether it existed.
func RemoveAutostart() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open autostart key: %w", err)
	}
	defer k.Close()
	err = k.DeleteValue(RunValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ---- per-user PATH ------------------------------------------------------------

// samePathEntry reports whether a PATH entry names dir. Entries may hold
// %VARIABLES% (the user PATH is usually REG_EXPAND_SZ) and a trailing slash.
func samePathEntry(entry, dir string) bool {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return false
	}
	if expanded, err := registry.ExpandString(entry); err == nil {
		entry = expanded
	}
	return strings.EqualFold(filepath.Clean(entry), filepath.Clean(dir))
}

// pathWith returns the PATH value with dir appended, unless it is there.
func pathWith(value, dir string) string {
	if pathHas(value, dir) {
		return value
	}
	value = strings.TrimRight(value, ";")
	if value == "" {
		return dir
	}
	return value + ";" + dir
}

// pathWithout returns the PATH value with every entry for dir removed.
func pathWithout(value, dir string) string {
	var keep []string
	for _, entry := range strings.Split(value, ";") {
		if entry != "" && !samePathEntry(entry, dir) {
			keep = append(keep, entry)
		}
	}
	return strings.Join(keep, ";")
}

func pathHas(value, dir string) bool {
	for _, entry := range strings.Split(value, ";") {
		if samePathEntry(entry, dir) {
			return true
		}
	}
	return false
}

// userPath reads the per-user PATH (HKCU\Environment\Path) and its type.
func userPath() (string, uint32, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, "Environment", registry.QUERY_VALUE)
	if err != nil {
		return "", registry.EXPAND_SZ, err
	}
	defer k.Close()
	value, kind, err := k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return "", registry.EXPAND_SZ, nil
	}
	return value, kind, err
}

// setUserPath writes the per-user PATH, keeping its type, and tells running
// programs (Explorer, so new terminals) that the environment changed.
func setUserPath(value string, kind uint32) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, "Environment", registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("open environment key: %w", err)
	}
	defer k.Close()
	if kind == registry.SZ {
		err = k.SetStringValue("Path", value)
	} else {
		err = k.SetExpandStringValue("Path", value)
	}
	if err != nil {
		return fmt.Errorf("set PATH: %w", err)
	}
	env := windows.StringToUTF16Ptr("Environment")
	const hwndBroadcast, wmSettingChange, smtoAbortIfHung = 0xffff, 0x001A, 0x0002
	_, _, _ = procSendMessageTimeout.Call(hwndBroadcast, wmSettingChange, 0,
		uintptr(unsafe.Pointer(env)), smtoAbortIfHung, 2000, 0)
	return nil
}

// InUserPath reports whether dir is on the per-user PATH.
func InUserPath(dir string) bool {
	value, _, err := userPath()
	return err == nil && pathHas(value, dir)
}

// AddToUserPath appends dir to the per-user PATH so new terminals find the
// programs in it. No administrator rights are needed.
func AddToUserPath(dir string) error {
	value, kind, err := userPath()
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("read PATH: %w", err)
	}
	next := pathWith(value, dir)
	if next == value {
		return nil
	}
	return setUserPath(next, kind)
}

// RemoveFromUserPath drops dir from the per-user PATH the installer (or the
// app's PATH checkbox) extended and tells running programs.
func RemoveFromUserPath(dir string) error {
	value, kind, err := userPath()
	if err != nil {
		return nil // no user PATH: nothing to remove
	}
	next := pathWithout(value, dir)
	if next == value {
		return nil
	}
	return setUserPath(next, kind)
}

var procSendMessageTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("SendMessageTimeoutW")

// AutostartEnabled reports whether the autostart entry exists.
func AutostartEnabled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	_, _, err = k.GetStringValue(RunValueName)
	return err == nil
}

// ---- single instance and control events -------------------------------------

// Instance is the tray app's single-instance guard and control channel.
type Instance struct {
	mutex   windows.Handle
	Quit    windows.Handle
	Restart windows.Handle
	Show    windows.Handle
}

// ErrAlreadyRunning is returned by Acquire when another tray app runs.
var ErrAlreadyRunning = errors.New("jikunode-tray is already running")

// Acquire claims the single-instance mutex, retrying for up to wait (a tray
// app that restarts itself releases the mutex just before the new one starts),
// and creates the quit, restart and show events.
func Acquire(wait time.Duration) (*Instance, error) {
	deadline := time.Now().Add(wait)
	for {
		h, err := windows.CreateMutex(nil, true, windows.StringToUTF16Ptr(trayMutexName))
		if err == nil {
			inst := &Instance{mutex: h}
			if inst.Quit, err = createEvent(quitEventName); err == nil {
				if inst.Restart, err = createEvent(restartEventName); err == nil {
					inst.Show, err = createEvent(showEventName)
				}
			}
			if err != nil {
				inst.Close()
				return nil, err
			}
			return inst, nil
		}
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, fmt.Errorf("create instance mutex: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, ErrAlreadyRunning
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func createEvent(name string) (windows.Handle, error) {
	// Auto-reset, initially unsignalled.
	h, err := windows.CreateEvent(nil, 0, 0, windows.StringToUTF16Ptr(name))
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return 0, fmt.Errorf("create event %s: %w", name, err)
	}
	return h, nil
}

// Close releases the mutex and events so a successor can start.
func (i *Instance) Close() {
	for _, h := range []*windows.Handle{&i.Quit, &i.Restart, &i.Show} {
		if *h != 0 {
			_ = windows.CloseHandle(*h)
			*h = 0
		}
	}
	if i.mutex != 0 {
		_ = windows.ReleaseMutex(i.mutex)
		_ = windows.CloseHandle(i.mutex)
		i.mutex = 0
	}
}

// signal sets a named event and reports whether a tray app was listening.
func signal(name string) bool {
	h, err := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, windows.StringToUTF16Ptr(name))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	return windows.SetEvent(h) == nil
}

// TrayRunning reports whether a tray app holds the instance mutex.
func TrayRunning() bool {
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, windows.StringToUTF16Ptr(trayMutexName))
	if err != nil {
		return false
	}
	_ = windows.CloseHandle(h)
	return true
}

// RequestQuit asks a running tray app to stop the node and exit, and waits up
// to wait for it to go. It reports whether a tray app was running.
func RequestQuit(wait time.Duration) bool {
	if !signal(quitEventName) {
		return false
	}
	deadline := time.Now().Add(wait)
	for TrayRunning() && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

// RequestRestart asks a running tray app to restart itself and the node from
// the binaries now on disk. It reports whether a tray app was running.
func RequestRestart() bool { return signal(restartEventName) }

// RequestShow asks a running tray app to open its window. It reports whether
// a tray app was running.
func RequestShow() bool { return signal(showEventName) }

// ---- processes ----------------------------------------------------------------

// HideConsole makes cmd start without a console window.
func HideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}

// Detach makes cmd outlive its parent without a console window.
func Detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW | windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// KillProgramsIn terminates every process (other than this one) whose image
// is one of names inside dir. It returns how many were stopped.
func KillProgramsIn(dir string, names ...string) int {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(snap)
	self := uint32(os.Getpid())
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	killed := 0
	for err = windows.Process32First(snap, &entry); err == nil; err = windows.Process32Next(snap, &entry) {
		if entry.ProcessID == self {
			continue
		}
		exe := windows.UTF16ToString(entry.ExeFile[:])
		match := false
		for _, n := range names {
			if strings.EqualFold(exe, n) {
				match = true
			}
		}
		if !match {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, entry.ProcessID)
		if err != nil {
			continue
		}
		path := imagePath(h)
		if path != "" && strings.EqualFold(filepath.Dir(path), filepath.Clean(dir)) {
			if windows.TerminateProcess(h, 1) == nil {
				_, _ = windows.WaitForSingleObject(h, 5000)
				killed++
			}
		}
		_ = windows.CloseHandle(h)
	}
	return killed
}

func imagePath(h windows.Handle) string {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}
