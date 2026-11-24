//go:build windows

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jiku-app/jikunode/internal/winapp"
)

const (
	// binaryName is the installed executable name uninstall may remove.
	binaryName = winapp.NodeExe
	// purgeNote is printed after --purge.
	purgeNote = ""
)

// configRoot is $XDG_CONFIG_HOME, data\config in a portable copy, or
// %APPDATA% (roaming).
func configRoot() (string, error) { return winapp.ConfigRoot() }

// stateRoot is $XDG_STATE_HOME, data\state in a portable copy, or
// %LOCALAPPDATA%.
func stateRoot() (string, error) { return winapp.StateRoot() }

// purgePaths are the config directory (key, config) and the local directory
// (upgrade lock, tray logs).
func purgePaths() ([]string, error) {
	config, err := configDir()
	if err != nil {
		return nil, err
	}
	state, err := stateRoot()
	if err != nil {
		return nil, err
	}
	return []string{config, filepath.Join(state, configDirName)}, nil
}

// stopUserService asks the tray app to stop the node and quit, then stops any
// node or tray process left running from the install directory.
func stopUserService(context.Context) {
	winapp.RequestQuit(10 * time.Second)
	if dir, err := winapp.InstallDir(); err == nil {
		winapp.KillProgramsIn(dir, winapp.NodeExe, winapp.TrayExe)
	}
	if _, portable := winapp.PortableDataDir(); portable {
		if exe, err := os.Executable(); err == nil {
			winapp.KillProgramsIn(filepath.Dir(exe), winapp.NodeExe, winapp.TrayExe)
		}
	}
}

// removeUserService removes the tray app's autostart entry and the install
// directory from the user's PATH.
func removeUserService() (bool, error) {
	if dir, err := winapp.InstallDir(); err == nil {
		_ = winapp.RemoveFromUserPath(dir)
	}
	if _, portable := winapp.PortableDataDir(); portable {
		if exe, err := os.Executable(); err == nil {
			_ = winapp.RemoveFromUserPath(filepath.Dir(exe))
		}
	}
	if programs, err := winapp.StartMenuDir(); err == nil {
		_, _ = removeIfExists(filepath.Join(programs, "JikuNode.lnk"))
	}
	return winapp.RemoveAutostart()
}

// removeInstalledBinary removes the installed programs. Windows cannot delete
// a running executable (this one), so deletion is handed to a detached,
// windowless cmd.exe that waits for this process to exit. Only the known
// program files are deleted, and the directory only if it is then empty.
func removeInstalledBinary(path string) (bool, error) {
	dir := filepath.Dir(path)
	if install, err := winapp.InstallDir(); err != nil || !strings.EqualFold(filepath.Clean(dir), filepath.Clean(install)) {
		// Not the per-user install (a developer build, say): leave it.
		return false, nil
	}
	var del []string
	for _, name := range []string{winapp.NodeExe, winapp.TrayExe, "jikunode.prev.exe", "jikunode-tray.prev.exe"} {
		del = append(del, `del /f /q "`+filepath.Join(dir, name)+`"`)
	}
	del = append(del, `del /f /q "`+filepath.Join(dir, ".jikunode-*")+`"`, `rmdir "`+dir+`"`)
	// ping gives this process time to exit before the delete runs.
	script := "ping -n 3 127.0.0.1 >nul & " + strings.Join(del, " & ")
	cmd := exec.Command("cmd.exe")
	winapp.Detach(cmd)
	cmd.SysProcAttr.CmdLine = `cmd.exe /d /c "` + script + `"`
	if err := cmd.Start(); err != nil {
		return false, err
	}
	_ = cmd.Process.Release()
	return true, nil
}

// afterPurge removes a portable copy's data folders once they are empty.
func afterPurge() {
	if data, portable := winapp.PortableDataDir(); portable {
		for _, dir := range []string{filepath.Join(data, "config"), filepath.Join(data, "state"), data} {
			_ = os.Remove(dir) // fails, harmlessly, unless empty
		}
	}
}

// uninstallNote tells the user of a portable copy that its programs stay.
func uninstallNote() string {
	if _, portable := winapp.PortableDataDir(); portable {
		return "jikunode: this is a portable copy; delete its folder to remove the programs"
	}
	return ""
}

// restartUpgradedService asks the tray app to restart itself and the node
// from the upgraded binaries. Without a tray app there is nothing to restart.
var restartUpgradedService = func(context.Context) error {
	winapp.RequestRestart()
	return nil
}

// isBinaryName reports whether name is the installed executable name
// (Windows file names are case-insensitive).
func isBinaryName(name string) bool { return strings.EqualFold(name, binaryName) }
