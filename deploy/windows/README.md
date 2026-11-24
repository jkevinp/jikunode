# JikuNode on Windows

JikuNode runs on Windows 10 and 11 (amd64 and arm64) as a per-user startup
app. No administrator rights or Windows service are involved.

- `jikunode.exe` is the same command-line agent as on Linux.
- `jikunode-tray.exe` is the JikuNode app. It starts when you sign in (from
  the `JikuNode` value under `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`),
  runs `jikunode.exe run` without a console window, and restarts it five
  seconds after it exits. It has a window and a notification-area icon.

## Install

1. Download `jikunode_<version>_windows_<arch>.zip`, `checksums.txt`,
   `checksums.txt.sig` and `install-user.ps1` from the release.
2. Run the installer from PowerShell. With `-Checksums` it checks the zip's
   SHA-256 against `checksums.txt` (verify the signature on `checksums.txt`
   yourself first):

   ```powershell
   powershell -ExecutionPolicy Bypass -File .\install-user.ps1 .\jikunode_<version>_windows_amd64.zip -Checksums .\checksums.txt
   ```

   The script copies both programs to `%LOCALAPPDATA%\Programs\jikunode`, adds
   that folder to your PATH, registers autostart, adds a JikuNode entry to the
   Start menu and starts the app with its window open.
   Options: `-Port <n>` if port 16888 is already used on this computer, and
   `-NoStart` to start at the next sign-in instead of now.
3. Bind the node: in the JikuNode window, type the binding code from the
   provider console (and a name for this computer, if you like) and press
   **Bind**. The node starts and the icon turns green once it is connected.
   From a terminal, `jikunode bind <code> --name office-pc` does the same.

## Portable copy (no install)

To run JikuNode from a folder you choose, such as a USB drive, without the
installer:

1. Download `jikunode_<version>_windows_<arch>_portable.zip` (check it
   against `checksums.txt`) and extract it to that folder.
2. Start `jikunode-tray.exe` from there, enter the binding code and press
   **Bind**.

The portable zip holds the two programs and an empty file named
`jikunode.portable`. That file is what makes the copy portable: the config,
node key, logs and preferences then live in a `data` folder next to the
programs, not in your user profile, and the copy is not added to PATH or the
Start menu. You can turn any copy into a portable one by creating that file
next to `jikunode.exe`. Moving the folder to another computer moves the node
identity with it.

"Start JikuNode when I sign in to Windows" still works for a portable copy as
long as the folder stays at the same path (for a USB drive: the same drive
letter). To remove a portable copy, quit JikuNode, untick start at sign-in
first if you turned it on, and delete the folder.

On a drive formatted FAT32 or exFAT (common for USB sticks) Windows keeps no
access lists, so the node key in `data` cannot be limited to your account:
anyone who has the drive can read it and act as this node. Prefer an
NTFS-formatted drive, or keep the drive safe.

## Build from source

You need [Go](https://go.dev/dl/) (the version in `go.mod`, or newer) and Git.
No C compiler is needed: both programs build with `CGO_ENABLED=0`.

```powershell
git clone https://github.com/jkevinp/jikunode-windows.git
cd jikunode-windows
.\deploy\windows\build.ps1
```

This builds both programs for this computer's architecture into
`dist\jikunode_dev_windows_amd64\` (`dist` is not tracked by Git). If
PowerShell refuses to run the script, start it with
`powershell -ExecutionPolicy Bypass -File .\deploy\windows\build.ps1`.

| Option | Effect |
|---|---|
| `-Version 0.6.0` | Stamps the version into `jikunode.exe` (shown by `jikunode version` and the status page) and names the folder `jikunode_0.6.0_windows_<arch>`. Without it the default from `cmd/jikunode/main.go` stays. |
| `-Arch arm64` | Builds for `amd64` or `arm64` instead of this computer's architecture. |
| `-Portable` | Adds the `jikunode.portable` marker, making a portable copy (folder name ends in `_portable`). |
| `-Zip` | Also zips the folder, in the same layout as the release zips. |
| `-Out <folder>` | Writes somewhere other than `dist`. |

Then either:

- **Run it portable:** build with `-Portable` and start `jikunode-tray.exe`
  from the output folder.
- **Install it:** build with `-Zip` and pass the zip to the installer:

  ```powershell
  .\deploy\windows\build.ps1 -Version 0.6.0 -Zip
  .\deploy\windows\install-user.ps1 .\dist\jikunode_0.6.0_windows_amd64.zip
  ```

To replace a copy that is running, quit JikuNode first (right-click the tray
icon, **Quit JikuNode**) so Windows lets the programs be overwritten. The
binding, key and preferences are not touched.

The script runs the same `go build` commands as the release workflow. By hand,
or from Linux or macOS:

```sh
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -X main.Version=0.6.0" -o jikunode.exe ./cmd/jikunode
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -ldflags "-s -w -H=windowsgui" -o jikunode-tray.exe ./cmd/jikunode-tray
```

`-H=windowsgui` matters for the tray app: without it a console window opens
next to it.

Before pushing, run the tests and checks for every platform the code builds
for:

```powershell
go test ./...
go vet ./...
$env:GOOS='linux'; go vet ./...; $env:GOOS='darwin'; go vet ./...; Remove-Item env:GOOS
gofmt -l .
```

`gofmt -l` lists files that are not formatted; format the ones you changed
with `gofmt -w <file>`. Two upstream test files in `cmd/jikunode` are listed
as well; leave them as they are so upstream merges stay clean.

### Updating from the upstream project

This repository is the upstream
[jiku-app/jikunode](https://github.com/jiku-app/jikunode) code plus the
Windows support. To bring in upstream changes, merge them, test, rebuild and
push:

```powershell
git remote add upstream https://github.com/jiku-app/jikunode.git   # once
git fetch upstream
git merge upstream/main
go test ./...
.\deploy\windows\build.ps1 -Version <new version>
git push
```

The Windows code is mostly in its own files (`*_windows.go`,
`cmd/jikunode-tray`, `internal/winapp`, `internal/winacl`, `deploy/windows`),
so conflicts are rare. The likeliest places are the shared files the Windows
support touches: `cmd/jikunode/uninstall.go`, the platform split in
`cmd/jikunode/platform_*.go`, `.github/workflows/release.yml` and the READMEs.

Hub-triggered upgrades download releases from the repository and signing key
set in `upgrade/upgrade.go`, currently the upstream project, which publishes
no Windows builds. Until it does (or this repository publishes its own signed
releases), update Windows machines by rebuilding and replacing the programs.

## The JikuNode window

Click the JikuNode icon in the notification area, or start JikuNode from the
Start menu, to open the window. It shows whether the node is connected, the
hub, the node id and version, and has:

- **Start node / Stop node**: Stop disconnects from the hub and stops the
  node; it stays stopped until you press Start (or restart JikuNode with
  "Connect automatically" on). Closing the window does not stop the node.
- **Pause / Resume**: asks the hub to stop or resume sending traffic, like
  `jikunode pause`, while the node stays connected.
- **Start JikuNode when I sign in to Windows**: the Run key entry. At sign-in
  JikuNode starts in the notification area without opening the window.
- **Connect to the hub automatically when JikuNode starts**: when off,
  JikuNode starts with the node stopped and waits for Start. Saved in
  `%LOCALAPPDATA%\jikunode\tray.json`.
- A **Node log** box with the end of the node's log, and the reason in red when
  the node cannot start (for example, another program already uses its port).
- **Add jikunode to PATH (use it in a terminal)**: adds the folder holding
  the programs to your per-user PATH, so `jikunode status`, `jikunode logs`
  and the other commands work in terminals opened afterwards; untick it to
  remove the folder again. The installer turns it on; for a portable copy it
  starts off, and it adds that copy's own folder.
- **Status port**: the loopback port of the node's status page (16888 by
  default). If another program already uses it, type another port and press
  **Apply**; JikuNode restarts the node on it and remembers it. `--port` on
  the command line overrides it until you press Apply.
- **Binding code** and **Name** boxes with a **Bind** button, shown while the
  node is not bound yet.
- **More ▾**, a menu with **Open status page** and **Open log folder**. The
  same menu opens when you right-click the window.
- **Minimize to tray** hides the window; JikuNode keeps running in the
  notification area. The title bar's minimize and close buttons do the same.

Together the two preferences decide what happens when Windows starts: both on
means the node is connected as soon as you sign in.

## Tray icon

| Colour | Meaning |
|---|---|
| Green | Connected and serving |
| Amber | Connecting, starting or paused |
| Red | The hub cannot be reached (the menu shows the last error) |
| Grey | Stopped, not bound yet, or about to restart after a crash |

Right-click the icon for the menu: Open JikuNode, Start/Stop node,
Pause/Resume, Restart node, the status page and log folder, the two
preferences, and Quit. Quit stops the node and closes JikuNode until you start
it again from the Start menu or sign in again (if start at sign-in is on).

## Files

Installed copy (a portable copy keeps all of this in `data\config` and
`data\state` next to its programs):

| What | Where |
|---|---|
| Programs | `%LOCALAPPDATA%\Programs\jikunode` |
| Config, node id and private key | `%APPDATA%\jikunode` |
| Node and tray logs (`node.log`, `tray.log`) | `%LOCALAPPDATA%\jikunode\logs` |
| App preferences (`tray.json`) and upgrade lock | `%LOCALAPPDATA%\jikunode` |

The config folder and key get an access list that allows only your account
and SYSTEM. The node refuses to load a key that any other account can read,
the Windows counterpart of the `0600` check on Linux.

## Upgrade and uninstall

Hub-triggered upgrades work as on Linux: the node installs the signed release,
exits, and the tray app starts the new version (restarting itself too when the
tray binary changed). `jikunode upgrade` does the same on demand.

```powershell
jikunode uninstall           # stop JikuNode; remove autostart, Start menu entry, PATH entry and programs
jikunode uninstall --purge   # also remove the config, key and logs
```

## Limits

- The node runs only while you are signed in. For an always-on machine, turn
  on automatic sign-in, or run `jikunode run` from a scheduled task or a
  service wrapper of your choice.
- Each Windows user who installs JikuNode gets their own node identity.
