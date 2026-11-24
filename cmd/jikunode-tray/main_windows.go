//go:build windows

// Command jikunode-tray is the Windows app for jikunode. It starts at sign-in
// (per-user Run key, no administrator rights), runs `jikunode.exe run`
// without a console window and restarts it when it exits. Its window and
// notification-area menu show the node's state and let the user start and
// stop the node, pause and resume it, and choose whether JikuNode starts at
// sign-in and whether it connects as soon as it starts.
//
// Build it with -ldflags -H=windowsgui so no console window opens.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/systray"
	"golang.org/x/sys/windows"

	"github.com/jiku-app/jikunode/internal/winapp"
	"github.com/jiku-app/jikunode/nodeagent"
	"github.com/jiku-app/jikunode/upgrade"
)

const (
	// pollInterval is how often the tray refreshes the node state.
	pollInterval = 3 * time.Second
	// noticeFor is how long an action's error stays on screen.
	noticeFor = 15 * time.Second
	// backgroundFlag starts the tray without opening its window; the Run key
	// and self-restarts use it.
	backgroundFlag = "--background"
)

type app struct {
	inst         *winapp.Instance
	sup          *supervisor
	client       *statusClient // guarded by mu: the port can change
	logger       *log.Logger
	logDir       string
	settingsPath string
	configDir    string // the node's config folder, to tell whether it is bound
	portable     bool
	selfPath     string
	selfStamp    fileStamp
	port         int  // guarded by mu
	portFlag     bool // port came from --port (kept for autostart)
	background   bool
	win          *window

	mu        sync.Mutex
	settings  settings
	notice    string
	noticeErr bool
	noticeAt  time.Time
	busy      atomic.Bool // a pause or resume is in flight
	binding   atomic.Bool // a bind is in flight
	refreshCh chan struct{}

	// restartSelf makes shutdown start a fresh tray from the binary on disk.
	restartSelf atomic.Bool
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
}

func main() {
	fs := flag.NewFlagSet("jikunode-tray", flag.ContinueOnError)
	port := fs.Int("port", defaultStatusPort, "loopback status port passed to jikunode run")
	background := fs.Bool("background", false, "start in the notification area without opening the window")
	if err := fs.Parse(os.Args[1:]); err != nil || fs.NArg() != 0 || *port <= 0 || *port > 65535 {
		fatal(errors.New("usage: jikunode-tray [--port PORT] [--background]"))
	}
	portFlag := false
	fs.Visit(func(f *flag.Flag) { portFlag = portFlag || f.Name == "port" })
	if !portFlag {
		*port = 0 // use the saved port, or the default
	}
	enableDPIAwareness()
	a, err := setup(*port, *background)
	if err != nil {
		if errors.Is(err, winapp.ErrAlreadyRunning) {
			// Started again from the Start menu: show the running app.
			if !*background {
				winapp.RequestShow()
			}
			os.Exit(0)
		}
		fatal(err)
	}
	// systray may run onExit on whichever goroutine called Quit while the
	// message loop ends and Run returns, so the real shutdown happens here,
	// on the main goroutine, where the process is sure to wait for it.
	systray.Run(a.onReady, nil)
	a.shutdown()
}

func setup(port int, background bool) (*app, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// A tray that restarts itself releases the instance lock before its
	// successor starts; the short wait covers a slow exit.
	inst, err := winapp.Acquire(3 * time.Second)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*app, error) {
		inst.Close()
		return nil, err
	}
	logDir, err := winapp.LogDir()
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return fail(err)
	}
	trayLog, err := openLog(filepath.Join(logDir, "tray.log"))
	if err != nil {
		return fail(err)
	}
	logger := log.New(io.Writer(trayLog), "", log.LstdFlags)
	// The tray has no console: send any crash report to the log as well.
	_ = debug.SetCrashOutput(trayLog, debug.CrashOptions{})
	upgrade.CleanupStale(self)

	nodePath := filepath.Join(filepath.Dir(self), winapp.NodeExe)
	if _, err := os.Stat(nodePath); err != nil {
		return fail(fmt.Errorf("%s not found next to the tray app", winapp.NodeExe))
	}
	// tray.json lives in the jikunode state folder, next to logs\.
	settingsPath := filepath.Join(filepath.Dir(logDir), "tray.json")
	prefs, err := loadSettings(settingsPath)
	if err != nil {
		logger.Printf("settings: %v (using defaults)", err)
	}
	configRoot, err := winapp.ConfigRoot()
	if err != nil {
		return fail(err)
	}
	_, portable := winapp.PortableDataDir()
	portFlag := port != 0
	if !portFlag {
		port = defaultStatusPort
		if validPort(prefs.Port) {
			port = prefs.Port
		}
	}
	a := &app{
		inst:         inst,
		client:       newStatusClient(port),
		port:         port,
		portFlag:     portFlag,
		background:   background,
		logger:       logger,
		logDir:       logDir,
		settingsPath: settingsPath,
		configDir:    filepath.Join(configRoot, winapp.AppDirName),
		portable:     portable,
		settings:     prefs,
		selfPath:     self,
		selfStamp:    stampOf(self),
		refreshCh:    make(chan struct{}, 1),
		done:         make(chan struct{}),
	}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.sup = newSupervisor(nodePath, logDir, logger, prefs.ConnectOnStart)
	a.sup.args = nodeArgs(port)
	a.sup.changed = func() bool { return stampOf(a.selfPath) != a.selfStamp }
	a.sup.onSelfUpdate = func() {
		a.restartSelf.Store(true)
		systray.Quit()
	}
	logger.Printf("tray started (pid %d, connect on start %v, portable %v, port %d)", os.Getpid(), prefs.ConnectOnStart, portable, port)
	return a, nil
}

// menu holds the notification-area menu items.
type menu struct {
	title, detail                   *systray.MenuItem
	toggle, pause, resume           *systray.MenuItem
	autostart, connect, path        *systray.MenuItem
	open, page, logs, restart, quit *systray.MenuItem
}

func (a *app) onReady() {
	icons := map[iconState][]byte{}
	for _, s := range []iconState{iconOff, iconConnecting, iconConnected, iconError} {
		icons[s] = renderIcon(s)
	}
	systray.SetIcon(icons[iconConnecting])
	systray.SetTooltip("JikuNode")

	var m menu
	m.open = systray.AddMenuItem("Open JikuNode", "Show the JikuNode window")
	systray.AddSeparator()
	m.title = systray.AddMenuItem("JikuNode: starting…", "")
	m.title.Disable()
	m.detail = systray.AddMenuItem("", "")
	m.detail.Disable()
	m.detail.Hide()
	systray.AddSeparator()
	m.toggle = systray.AddMenuItem("Stop node", "Disconnect from the hub and stop the node")
	m.pause = systray.AddMenuItem("Pause", "Ask the hub to stop sending traffic to this node")
	m.resume = systray.AddMenuItem("Resume", "Ask the hub to send traffic to this node again")
	m.resume.Hide()
	m.pause.Disable()
	m.restart = systray.AddMenuItem("Restart node", "")
	systray.AddSeparator()
	m.page = systray.AddMenuItem("Open status page", "Open the local status page in your browser")
	m.logs = systray.AddMenuItem("Open log folder", "")
	systray.AddSeparator()
	m.autostart = systray.AddMenuItemCheckbox("Start at sign-in", "Start JikuNode when you sign in to Windows", winapp.AutostartEnabled())
	m.connect = systray.AddMenuItemCheckbox("Connect when JikuNode starts", "Start the node and connect to the hub as soon as JikuNode starts", a.connectOnStart())
	m.path = systray.AddMenuItemCheckbox("Add jikunode to PATH", "Make the jikunode command work in new terminals", winapp.InUserPath(a.programDir()))
	systray.AddSeparator()
	m.quit = systray.AddMenuItem("Quit JikuNode", "Stop the node and close JikuNode")

	title := "JikuNode"
	if a.portable {
		title = "JikuNode (portable)"
	}
	win, err := newWindow(title, windowActions{
		toggleNode:   a.toggleNode,
		pause:        func() { a.control("pause") },
		resume:       func() { a.control("resume") },
		openPage:     func() { openShell(a.statusClient().pageURL()) },
		openLogs:     func() { openShell(a.logDir) },
		setAutostart: a.setAutostart,
		setConnect:   a.setConnect,
		bind:         a.bind,
		setPort:      a.setPort,
		setPath:      a.setPath,
	}, icons, a.logger)
	if err != nil {
		a.logger.Printf("window: %v (the menu still works)", err)
	}
	a.win = win
	// Left click opens the window; right click shows the menu.
	if win != nil {
		systray.SetOnTapped(win.show)
	}

	go func() {
		a.sup.run(a.ctx)
		close(a.done)
	}()
	go a.watchEvents()

	go func() {
		t := time.NewTicker(pollInterval)
		defer t.Stop()
		a.refresh(&m, icons)
		if !a.background {
			a.win.show()
		}
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-t.C:
			case <-a.refreshCh:
			case <-m.open.ClickedCh:
				a.win.show()
				continue
			case <-m.toggle.ClickedCh:
				a.toggleNode()
			case <-m.pause.ClickedCh:
				m.pause.Disable()
				go a.control("pause")
				continue
			case <-m.resume.ClickedCh:
				m.resume.Disable()
				go a.control("resume")
				continue
			case <-m.restart.ClickedCh:
				a.logger.Printf("restart requested from menu")
				a.sup.restart()
			case <-m.page.ClickedCh:
				openShell(a.statusClient().pageURL())
				continue
			case <-m.logs.ClickedCh:
				openShell(a.logDir)
				continue
			case <-m.autostart.ClickedCh:
				a.setAutostart(!m.autostart.Checked())
			case <-m.connect.ClickedCh:
				a.setConnect(!m.connect.Checked())
			case <-m.path.ClickedCh:
				a.setPath(!m.path.Checked())
			case <-m.quit.ClickedCh:
				systray.Quit()
				return
			}
			a.refresh(&m, icons)
		}
	}()
}

// refresh polls the node and updates the icon, the menu and the window.
func (a *app) refresh(m *menu, icons map[iconState][]byte) {
	wanted, up := a.sup.wantRunning(), a.sup.running()
	var st *nodeStatus
	err := errors.New("node not running")
	if wanted || up {
		pctx, pcancel := context.WithTimeout(a.ctx, 2*time.Second)
		st, err = a.statusClient().status(pctx)
		pcancel()
	}
	v := describe(st, err, up, wanted)
	if wanted && !up && err != nil {
		if f := a.sup.lastFailure(); f != "" {
			v = failureView(f, a.currentPort())
		}
	}
	logText, logErr := readLogTail(a.sup.logPath)
	if logErr != nil && !errors.Is(logErr, os.ErrNotExist) {
		logText = "Could not read " + a.sup.logPath + ": " + logErr.Error()
	}
	busy := a.busy.Load()
	autostart := winapp.AutostartEnabled()
	onPath := winapp.InUserPath(a.programDir())
	connect := a.connectOnStart()
	notice, noticeErr := a.currentNotice()

	systray.SetIcon(icons[v.icon])
	systray.SetTooltip(v.title)
	if notice != "" {
		m.title.SetTitle("JikuNode: " + truncate(notice, 50))
	} else {
		m.title.SetTitle(v.title)
	}
	if v.detail != "" {
		m.detail.SetTitle(v.detail)
		m.detail.Show()
	} else {
		m.detail.Hide()
	}
	if wanted {
		m.toggle.SetTitle("Stop node")
		m.toggle.SetTooltip("Disconnect from the hub and stop the node")
	} else {
		m.toggle.SetTitle("Start node")
		m.toggle.SetTooltip("Start the node and connect to the hub")
	}
	if v.canResume {
		m.pause.Hide()
		m.resume.Show()
		setEnabled(m.resume, !busy)
	} else {
		m.resume.Hide()
		m.pause.Show()
		setEnabled(m.pause, v.canPause && !busy)
	}
	setChecked(m.autostart, autostart)
	setChecked(m.path, onPath)
	setChecked(m.connect, connect)

	bound := nodeagent.IsBound(a.configDir) || (st != nil && err == nil && st.Bound)
	ws := windowState{view: v, wanted: wanted, busy: busy, notice: notice, noticeErr: noticeErr, autostart: autostart, connect: connect,
		bound: bound, binding: a.binding.Load(), log: logText, port: a.currentPort(), onPath: onPath}
	if st != nil && err == nil {
		ws.nodeID, ws.version = st.NodeID, st.Version
	}
	a.win.update(ws)
}

func (a *app) refreshSoon() {
	select {
	case a.refreshCh <- struct{}{}:
	default:
	}
}

// bind runs `jikunode bind` with the code and name typed in the window and
// starts the node, which then connects to the hub.
func (a *app) bind(code, name string) {
	a.binding.Store(true)
	a.refreshSoon()
	defer func() {
		a.binding.Store(false)
		a.refreshSoon()
	}()
	args := []string{"bind", code}
	if name != "" {
		args = append(args, "--name", name)
	}
	ctx, cancel := context.WithTimeout(a.ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.sup.nodePath, args...)
	cmd.Dir = filepath.Dir(a.sup.nodePath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	winapp.HideConsole(cmd)
	if err := cmd.Run(); err != nil {
		msg := strings.TrimPrefix(lastLine(stderr.String()), "jikunode: ")
		if msg == "" {
			msg = err.Error()
		}
		a.logger.Printf("bind failed: %s", msg) // the code itself is not logged
		a.setError("Bind failed: " + msg)
		return
	}
	a.logger.Printf("bind: %s", firstLine(stdout.String()))
	a.setNotice("Bound. Connecting to the hub…")
	if !a.sup.wantRunning() {
		a.sup.start()
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return strings.TrimSpace(s)
}

// toggleNode stops a running node or starts a stopped one.
func (a *app) toggleNode() {
	if a.sup.wantRunning() {
		a.sup.stop()
	} else {
		a.sup.start()
	}
	a.refreshSoon()
}

// control sends pause or resume to the node.
func (a *app) control(action string) {
	a.busy.Store(true)
	a.refreshSoon()
	defer func() {
		a.busy.Store(false)
		a.refreshSoon()
	}()
	cctx, ccancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer ccancel()
	if err := a.statusClient().control(cctx, action); err != nil {
		a.logger.Printf("%s: %v", action, err)
		a.setError(err.Error())
		return
	}
	a.setNotice("")
}

func (a *app) setAutostart(on bool) {
	defer a.refreshSoon()
	if !on {
		if _, err := winapp.RemoveAutostart(); err != nil {
			a.logger.Printf("disable autostart: %v", err)
			a.setError("Could not turn off start at sign-in: " + err.Error())
		}
		return
	}
	if err := winapp.SetAutostart(a.selfPath, append(a.args(), backgroundFlag)...); err != nil {
		a.logger.Printf("enable autostart: %v", err)
		a.setError("Could not turn on start at sign-in: " + err.Error())
	}
}

// programDir is the folder holding jikunode.exe and this app: the folder the
// PATH checkbox adds.
func (a *app) programDir() string { return filepath.Dir(a.sup.nodePath) }

// setPath adds the program folder to the per-user PATH, or removes it, so
// `jikunode` works in terminals opened afterwards.
func (a *app) setPath(on bool) {
	defer a.refreshSoon()
	dir := a.programDir()
	if !on {
		if err := winapp.RemoveFromUserPath(dir); err != nil {
			a.logger.Printf("remove from PATH: %v", err)
			a.setError("Could not remove from PATH: " + err.Error())
			return
		}
		a.logger.Printf("removed %s from PATH", dir)
		a.setNotice("Removed from PATH for new terminals.")
		return
	}
	if err := winapp.AddToUserPath(dir); err != nil {
		a.logger.Printf("add to PATH: %v", err)
		a.setError("Could not add to PATH: " + err.Error())
		return
	}
	a.logger.Printf("added %s to PATH", dir)
	a.setNotice("Added to PATH. Use jikunode in a new terminal.")
}

func (a *app) setConnect(on bool) {
	defer a.refreshSoon()
	a.mu.Lock()
	next := a.settings
	next.ConnectOnStart = on
	a.mu.Unlock()
	if err := saveSettings(a.settingsPath, next); err != nil {
		a.logger.Printf("save settings: %v", err)
		a.setError("Could not save the setting: " + err.Error())
		return
	}
	a.mu.Lock()
	a.settings = next
	a.mu.Unlock()
	a.logger.Printf("connect on start set to %v", on)
}

func (a *app) connectOnStart() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.settings.ConnectOnStart
}

// setNotice shows s (a result) for a while; setError shows an error.
func (a *app) setNotice(s string) { a.note(s, false) }
func (a *app) setError(s string)  { a.note(s, true) }

func (a *app) note(s string, isErr bool) {
	a.mu.Lock()
	a.notice, a.noticeErr, a.noticeAt = truncate(s, 120), isErr, time.Now()
	a.mu.Unlock()
}

func (a *app) currentNotice() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if time.Since(a.noticeAt) > noticeFor {
		a.notice = ""
	}
	return a.notice, a.noticeErr
}

// watchEvents reacts to `jikunode uninstall` (quit), `jikunode upgrade`
// (restart from the new binaries) and a second launch (show the window).
func (a *app) watchEvents() {
	handles := []windows.Handle{a.inst.Quit, a.inst.Restart, a.inst.Show}
	for a.ctx.Err() == nil {
		ev, err := windows.WaitForMultipleObjects(handles, false, 500)
		if err != nil {
			a.logger.Printf("control events: %v", err)
			return
		}
		switch ev {
		case windows.WAIT_OBJECT_0:
			a.logger.Printf("quit requested")
			systray.Quit()
			return
		case windows.WAIT_OBJECT_0 + 1:
			a.logger.Printf("restart requested")
			a.restartSelf.Store(true)
			systray.Quit()
			return
		case windows.WAIT_OBJECT_0 + 2:
			a.win.show()
		}
	}
}

// shutdown stops the node, releases the instance lock and, after an upgrade,
// starts the new tray binary.
func (a *app) shutdown() {
	a.win.destroy()
	a.cancel()
	select {
	case <-a.done:
	case <-time.After(10 * time.Second):
	}
	a.inst.Close()
	if a.restartSelf.Load() {
		cmd := exec.Command(a.selfPath, append(a.args(), backgroundFlag)...)
		winapp.Detach(cmd)
		if err := cmd.Start(); err != nil {
			a.logger.Printf("restart tray: %v", err)
		} else {
			a.logger.Printf("tray restarting (new pid %d)", cmd.Process.Pid)
		}
	}
	a.logger.Printf("tray stopped")
}

// args are the tray's own arguments, kept for autostart and self-restart.
func (a *app) args() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.portFlag {
		return nil // the port is in tray.json
	}
	return nodeArgs(a.port)
}

// nodeArgs are the `jikunode run` arguments for a status port.
func nodeArgs(port int) []string {
	if port == defaultStatusPort {
		return nil
	}
	return []string{"--port", strconv.Itoa(port)}
}

func (a *app) currentPort() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.port
}

func (a *app) statusClient() *statusClient {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.client
}

// setPort moves the node's status server to port: it checks the port is
// free, saves it in tray.json and restarts the node on it.
func (a *app) setPort(port int) {
	defer a.refreshSoon()
	if !validPort(port) {
		a.setError("Choose a port from 1024 to 65535")
		return
	}
	current := a.currentPort()
	if port != current {
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			a.setError(fmt.Sprintf("Port %d is used by another program; try another", port))
			return
		}
		_ = ln.Close()
	}
	a.mu.Lock()
	next := a.settings
	next.Port = port
	if port == defaultStatusPort {
		next.Port = 0
	}
	a.mu.Unlock()
	if err := saveSettings(a.settingsPath, next); err != nil {
		a.logger.Printf("save settings: %v", err)
		a.setError("Could not save the port: " + err.Error())
		return
	}
	a.mu.Lock()
	a.settings = next
	a.port, a.portFlag = port, false
	a.client = newStatusClient(port)
	a.mu.Unlock()
	a.logger.Printf("status port set to %d", port)
	// A Run key written with --port would override the saved port.
	if winapp.AutostartEnabled() {
		if err := winapp.SetAutostart(a.selfPath, backgroundFlag); err != nil {
			a.logger.Printf("update autostart: %v", err)
		}
	}
	if port == current {
		a.setNotice(fmt.Sprintf("Port %d saved", port))
		return
	}
	a.sup.setArgs(nodeArgs(port))
	if a.sup.wantRunning() {
		a.sup.restart()
		a.setNotice(fmt.Sprintf("Port set to %d. Restarting the node…", port))
	} else {
		a.setNotice(fmt.Sprintf("Port set to %d", port))
	}
}

func setEnabled(item *systray.MenuItem, on bool) {
	if on {
		item.Enable()
	} else {
		item.Disable()
	}
}

func setChecked(item *systray.MenuItem, on bool) {
	if on != item.Checked() {
		if on {
			item.Check()
		} else {
			item.Uncheck()
		}
	}
}

// openShell opens a URL or folder with its default handler.
func openShell(target string) {
	_ = windows.ShellExecute(0, windows.StringToUTF16Ptr("open"), windows.StringToUTF16Ptr(target), nil, nil, windows.SW_SHOWNORMAL)
}

// fatal reports a startup error in a message box: the tray has no console.
func fatal(err error) {
	_, _ = windows.MessageBox(0, windows.StringToUTF16Ptr("JikuNode could not start:\n\n"+err.Error()),
		windows.StringToUTF16Ptr("JikuNode"), windows.MB_OK|windows.MB_ICONERROR)
	os.Exit(1)
}
