package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// The "service" commands wrap `chatmem daemon` behind an OS-native user-level
// service manager: launchd on macOS, systemd --user on Linux. The service
// keeps embedded Postgres warm across MCP-client restarts (spawned MCP procs
// attach to the running PG per v0.3.1's tryAttachPostgres).
//
// Naming: install/uninstall/start/stop/restart/status are exposed at the
// top level (per user request) rather than under a `service` namespace.
// Their help text always clarifies they operate on the background service,
// not the binary itself.

const (
	// launchd label + macOS plist path.
	launchdLabel     = "dev.chatmem.daemon"
	launchdPlistDir  = "Library/LaunchAgents"
	launchdPlistFile = "dev.chatmem.daemon.plist"

	// systemd --user unit path.
	systemdUnitFile = "chatmem.service"
)

// ─── top-level cobra commands ──────────────────────────────────────────────

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install chatmem as a user-level background service (launchd on macOS, systemd --user on Linux)",
		Long: `Installs a user-level service that runs chatmem daemon at login and
restarts it on crash. Keeps embedded Postgres warm so MCP-client
invocations of chatmem mcp attach instantly instead of cold-starting PG.

macOS: writes ~/Library/LaunchAgents/dev.chatmem.daemon.plist and loads
      it via launchctl.
Linux: writes ~/.config/systemd/user/chatmem.service and enables + starts
      it via systemctl --user.

Does NOT install the chatmem binary itself — use brew / zypper / dnf for
that. This command is only about the background service.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNonRoot(); err != nil {
				return err
			}
			if err := preflight(); err != nil {
				return err
			}
			return installService()
		},
	}
}

func newUninstallCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Uninstall the chatmem background service (does NOT remove the binary)",
		Long: `Stops + removes the user-level service that was installed via
chatmem install. Existing data (~/.local/share/chatmem/) is left alone
unless --purge is passed.

Does NOT uninstall the chatmem binary itself — use brew uninstall / zypper
remove / dnf remove for that.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := uninstallService(); err != nil {
				return err
			}
			if purge {
				if err := purgeData(); err != nil {
					return fmt.Errorf("data purge: %w", err)
				}
				fmt.Println("data + cache directories removed")
			} else {
				fmt.Println("service removed. Data preserved at", dataHome())
				fmt.Println("(pass --purge next time to also delete the database)")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "also delete ~/.local/share/chatmem and cache")
	return cmd
}

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the chatmem background service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNonRoot(); err != nil {
				return err
			}
			if err := preflight(); err != nil {
				return err
			}
			if err := serviceInstalled(); err != nil {
				return err
			}
			return startService()
		},
	}
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the chatmem background service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := serviceInstalled(); err != nil {
				return err
			}
			return stopService()
		},
	}
}

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Stop then start the chatmem background service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := requireNonRoot(); err != nil {
				return err
			}
			if err := preflight(); err != nil {
				return err
			}
			if err := serviceInstalled(); err != nil {
				return err
			}
			_ = stopService() // best-effort; may already be stopped
			// Small gap so PG's shutdown checkpoint completes cleanly
			// before the fresh daemon claims the port.
			time.Sleep(500 * time.Millisecond)
			return startService()
		},
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether the chatmem background service is installed / running",
		RunE: func(cmd *cobra.Command, args []string) error {
			printServiceStatus()
			return nil
		},
	}
}

// ─── platform-agnostic helpers ─────────────────────────────────────────────

func serviceInstalled() error {
	path := serviceUnitPath()
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("chatmem service is not installed. Run: chatmem install")
	} else if err != nil {
		return err
	}
	return nil
}

// serviceUnitPath returns the OS-specific location of the service definition.
func serviceUnitPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, launchdPlistDir, launchdPlistFile)
	case "linux":
		return filepath.Join(home, ".config", "systemd", "user", systemdUnitFile)
	default:
		return ""
	}
}

// binPath finds the chatmem executable so the service unit points at the
// right absolute path (services can't rely on the user's PATH).
func binPath() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve chatmem executable path: %w", err)
	}
	// os.Executable returns a possibly-symlinked path; resolve to the real
	// binary so brew-cask upgrades (which relink) don't invalidate the unit.
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return self, nil
	}
	return resolved, nil
}

func installService() error {
	switch runtime.GOOS {
	case "darwin":
		return installLaunchd()
	case "linux":
		return installSystemd()
	default:
		return fmt.Errorf("chatmem service management is not implemented on %s yet", runtime.GOOS)
	}
}

func uninstallService() error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchd()
	case "linux":
		return uninstallSystemd()
	default:
		return fmt.Errorf("chatmem service management is not implemented on %s yet", runtime.GOOS)
	}
}

func startService() error {
	switch runtime.GOOS {
	case "darwin":
		return launchctlLoad()
	case "linux":
		return systemctlStart()
	default:
		return fmt.Errorf("unsupported OS")
	}
}

func stopService() error {
	switch runtime.GOOS {
	case "darwin":
		return launchctlUnload()
	case "linux":
		return systemctlStop()
	default:
		return fmt.Errorf("unsupported OS")
	}
}

// printServiceStatus produces a one-screen summary users can act on.
func printServiceStatus() {
	fmt.Printf("chatmem %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
	if bin, err := binPath(); err == nil {
		fmt.Printf("binary:   %s\n", bin)
	}
	fmt.Printf("data dir: %s\n", dataHome())
	fmt.Printf("cache:    %s\n", cacheHome())
	fmt.Println()

	if bin, err := binPath(); err == nil {
		if s := codesignStatus(bin); s != "" {
			fmt.Printf("signing:  %s\n", s)
		}
	}

	fmt.Println("── service ──")
	unit := serviceUnitPath()
	if unit == "" {
		fmt.Printf("state: unsupported OS (%s)\n", runtime.GOOS)
		return
	}
	if _, err := os.Stat(unit); errors.Is(err, os.ErrNotExist) {
		fmt.Println("state: NOT installed")
		fmt.Println("hint:  chatmem install")
	} else {
		fmt.Printf("state:      installed\n")
		fmt.Printf("unit file:  %s\n", unit)
		running := portListening(defaultPort)
		if running {
			fmt.Printf("postgres:   ✓ listening on 127.0.0.1:%d\n", defaultPort)
		} else {
			fmt.Printf("postgres:   ✗ not listening on 127.0.0.1:%d — run: chatmem start\n", defaultPort)
		}
	}
}

// portListening does a 500 ms TCP dial to check if something is listening.
// Not a chatmem-liveness probe — that's chatmem doctor's job.
func portListening(port uint32) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func purgeData() error {
	for _, p := range []string{dataHome(), cacheHome()} {
		if err := os.RemoveAll(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// ─── macOS launchd ─────────────────────────────────────────────────────────

const launchdPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key>
        <string>%s</string>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
    </dict>
    <key>StandardOutPath</key>
    <string>%s/chatmem.log</string>
    <key>StandardErrorPath</key>
    <string>%s/chatmem.log</string>
</dict>
</plist>
`

func installLaunchd() error {
	bin, err := binPath()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logDir := filepath.Join(home, "Library", "Logs", "chatmem")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(home, launchdPlistDir), 0o755); err != nil {
		return err
	}

	// Ad-hoc codesign so launchd's Gatekeeper check on every daemon start
	// doesn't prompt the user. This does NOT bypass the "downloaded from
	// internet" quarantine — that's what `xattr -d com.apple.quarantine`
	// (documented in the README) handles. Together, no more scary dialogs.
	// Release binaries in v0.3.2+ SHOULD also be Developer-ID-signed +
	// notarized in the release workflow; this local ad-hoc signing is the
	// safety net for pre-notarization installs.
	if err := adhocSign(bin); err != nil {
		fmt.Printf("warning: could not ad-hoc sign %s (%v)\n", bin, err)
		fmt.Println("         Gatekeeper may prompt on daemon starts. Try: sudo codesign --force --sign - " + bin)
	}

	body := fmt.Sprintf(launchdPlistTemplate, launchdLabel, bin, home, logDir, logDir)
	path := filepath.Join(home, launchdPlistDir, launchdPlistFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Printf("installed %s\n", path)
	fmt.Printf("logs: %s/chatmem.log\n", logDir)
	// Attempt to load right away; user gets an immediately-running daemon.
	if err := launchctlLoad(); err != nil {
		return fmt.Errorf("launchctl load: %w (unit file written; try `launchctl load %s` manually)", err, path)
	}
	fmt.Println("started via launchctl. Verify: chatmem status")
	return nil
}

// adhocSign runs `codesign --force --sign -` on the chatmem binary. Ad-hoc
// signatures establish a stable code identity that Gatekeeper accepts for
// locally-installed binaries without a Developer ID. Real Developer-ID +
// notarization removes the prompts for OTHER users too; this only helps
// the user who ran `chatmem install`.
func adhocSign(bin string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	// Verify first — if already signed, skip.
	if err := exec.Command("codesign", "--verify", "--strict", bin).Run(); err == nil {
		return nil // already signed (ad-hoc or real)
	}
	out, err := exec.Command("codesign", "--force", "--sign", "-", bin).CombinedOutput()
	if err != nil {
		return fmt.Errorf("codesign: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Printf("ad-hoc signed %s\n", bin)
	return nil
}

// codesignStatus returns "ad-hoc signed" | "Developer ID signed" |
// "not signed" | "check failed" for the chatmem binary. Used by doctor
// and status. macOS-only; returns "" on other OSes.
func codesignStatus(bin string) string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	if err := exec.Command("codesign", "--verify", "--strict", bin).Run(); err != nil {
		return "not signed"
	}
	// Look at the signing authority to distinguish ad-hoc from real.
	out, err := exec.Command("codesign", "-dv", "--verbose=2", bin).CombinedOutput()
	if err != nil {
		return "check failed"
	}
	s := string(out)
	if strings.Contains(s, "Signature=adhoc") {
		return "ad-hoc signed (locally trusted only)"
	}
	if strings.Contains(s, "Authority=Developer ID Application") {
		return "Developer ID signed"
	}
	if strings.Contains(s, "Apple Notary Authority") || strings.Contains(s, "runtime version") {
		return "Developer ID signed + notarized"
	}
	return "signed (unknown authority)"
}

func uninstallLaunchd() error {
	// Unload first (best-effort), then remove the plist.
	_ = launchctlUnload()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, launchdPlistDir, launchdPlistFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	fmt.Println("removed", path)
	return nil
}

func launchctlLoad() error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, launchdPlistDir, launchdPlistFile)
	out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func launchctlUnload() error {
	home, _ := os.UserHomeDir()
	path := filepath.Join(home, launchdPlistDir, launchdPlistFile)
	out, err := exec.Command("launchctl", "unload", "-w", path).CombinedOutput()
	if err != nil {
		// launchctl exits non-zero when the unit isn't loaded — treat as success.
		msg := strings.TrimSpace(string(out))
		if strings.Contains(msg, "Could not find specified service") ||
			strings.Contains(msg, "No such file or directory") ||
			strings.Contains(msg, "not currently loaded") {
			return nil
		}
		return fmt.Errorf("%w: %s", err, msg)
	}
	return nil
}

// ─── Linux systemd --user ──────────────────────────────────────────────────

const systemdUnitTemplate = `[Unit]
Description=chatmem daemon — keeps embedded Postgres warm for chatmem mcp clients
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s daemon
Restart=on-failure
RestartSec=5s
Environment=HOME=%s

[Install]
WantedBy=default.target
`

func installSystemd() error {
	bin, err := binPath()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body := fmt.Sprintf(systemdUnitTemplate, bin, home)
	path := filepath.Join(dir, systemdUnitFile)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Printf("installed %s\n", path)
	// daemon-reload so systemd picks up the new file, then enable+start.
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", systemdUnitFile).CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user enable --now: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("enabled + started via systemctl. Verify: chatmem status")
	// Try to enable lingering so the service persists after logout.
	// This may fail without sudo — that's fine; the unit still runs while
	// the user is logged in.
	_, _ = exec.Command("loginctl", "enable-linger").CombinedOutput()
	return nil
}

func uninstallSystemd() error {
	_ = systemctlStop()
	_, _ = exec.Command("systemctl", "--user", "disable", systemdUnitFile).CombinedOutput()
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".config", "systemd", "user", systemdUnitFile)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, _ = exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput()
	fmt.Println("removed", path)
	return nil
}

func systemctlStart() error {
	out, err := exec.Command("systemctl", "--user", "start", systemdUnitFile).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl --user start: %w: %s", err, strings.TrimSpace(string(out)))
	}
	fmt.Println("started")
	return nil
}

func systemctlStop() error {
	out, err := exec.Command("systemctl", "--user", "stop", systemdUnitFile).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		// Ignore "not loaded" errors so `chatmem stop` is idempotent.
		if strings.Contains(msg, "not loaded") || strings.Contains(msg, "could not be found") {
			return nil
		}
		return fmt.Errorf("systemctl --user stop: %w: %s", err, msg)
	}
	fmt.Println("stopped")
	return nil
}

