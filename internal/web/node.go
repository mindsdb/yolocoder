package web

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/mindsdb/yolocoder/internal/terminal"
)

var errNodeMissing = errors.New("Node.js is required for yolocoder --web; install it from https://nodejs.org and try again")

// ensureNode makes sure node and npm are on PATH before --web tries to
// scaffold or run anything that needs them. Discovering this partway
// through a scaffold or an npm install would leave the folder in a
// half-finished state; checking first means either it just works, or the
// user finds out immediately and gets offered the fix.
func ensureNode() error {
	if hasNode() {
		return nil
	}
	fmt.Println("[*_*] Node.js was not found on your PATH. yolocoder --web needs it to run and build the app.")
	if !terminal.IsTTY(os.Stdin) {
		return errNodeMissing
	}
	install, err := promptInstallNode()
	if err != nil || !install {
		return errNodeMissing
	}
	if err := installNode(); err != nil {
		return fmt.Errorf("could not install Node.js automatically: %w\n\nInstall it yourself from https://nodejs.org and run yolocoder --web again", err)
	}
	if !hasNode() {
		return fmt.Errorf("Node.js still isn't on PATH after installing; open a new terminal (so it picks up the updated PATH) and run yolocoder --web again")
	}
	fmt.Println("[^_^] Node.js is installed.")
	return nil
}

func hasNode() bool {
	return commandExists("node") && commandExists("npm")
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func promptInstallNode() (bool, error) {
	reader := terminal.NewReader(os.Stdin)
	choice, err := reader.Select(os.Stdout, []terminal.Choice{
		{Label: "Yes, install Node now", Detail: installMethod()},
		{Label: "No, I'll install it myself"},
	}, 0)
	if err != nil {
		return false, err
	}
	return choice == 0, nil
}

// installMethod describes what "Yes" is actually about to run, so the
// choice isn't a blind trust exercise.
func installMethod() string {
	switch runtime.GOOS {
	case "darwin":
		if commandExists("brew") {
			return "brew install node"
		}
		return "no installer found; you'll be pointed to nodejs.org instead"
	case "linux":
		if name, ok := linuxPackageManager(); ok {
			return "sudo " + name + " install node/nodejs"
		}
		return "no supported package manager found; you'll be pointed to nodejs.org instead"
	case "windows":
		if commandExists("winget") {
			return "winget install OpenJS.NodeJS.LTS"
		}
		return "winget not found; you'll be pointed to nodejs.org instead"
	default:
		return "unsupported OS; you'll be pointed to nodejs.org instead"
	}
}

// installNode runs the best available install method for this platform,
// inheriting the terminal so a package manager's own prompts (a sudo
// password, in particular) work exactly as they would run by hand.
func installNode() error {
	switch runtime.GOOS {
	case "darwin":
		if commandExists("brew") {
			return runInherited("brew", "install", "node")
		}
	case "linux":
		if name, ok := linuxPackageManager(); ok {
			return runInherited("sudo", linuxInstallArgs(name)...)
		}
	case "windows":
		if commandExists("winget") {
			return runInherited("winget", "install", "-e", "--id", "OpenJS.NodeJS.LTS")
		}
	}
	return fmt.Errorf("no supported installer found for %s; install Node from https://nodejs.org", runtime.GOOS)
}

func linuxPackageManager() (string, bool) {
	for _, name := range []string{"apt-get", "dnf", "pacman"} {
		if commandExists(name) {
			return name, true
		}
	}
	return "", false
}

func linuxInstallArgs(manager string) []string {
	switch manager {
	case "apt-get":
		return []string{"apt-get", "install", "-y", "nodejs", "npm"}
	case "dnf":
		return []string{"dnf", "install", "-y", "nodejs", "npm"}
	case "pacman":
		return []string{"pacman", "-S", "--noconfirm", "nodejs", "npm"}
	default:
		return nil
	}
}

func runInherited(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}
