// Package nativeui opens user-initiated host OS dialogs, without browser uploads
// or Apple Events to Finder/System Events. The CLI remains CGO-free.
package nativeui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

var ErrCancelled = errors.New("folder selection cancelled")
var dialogMu sync.Mutex

func DirectoryCommand(platform string) ([]string, error) {
	switch platform {
	case "darwin":
		return []string{"/usr/bin/osascript", "-l", "JavaScript", "-e", `ObjC.import('AppKit');
var app = $.NSApplication.sharedApplication;
app.setActivationPolicy($.NSApplicationActivationPolicyAccessory);
app.activateIgnoringOtherApps(true);
var panel = $.NSOpenPanel.openPanel;
panel.canChooseFiles = false;
panel.canChooseDirectories = true;
panel.allowsMultipleSelection = false;
panel.canCreateDirectories = false;
panel.title = '选择 MCPX 项目文件夹';
panel.prompt = '选择项目';
var answer = panel.runModal;
JSON.stringify(answer == $.NSModalResponseOK ? {path: ObjC.unwrap(panel.URL.path)} : {cancelled: true});`}, nil
	case "windows":
		return []string{"powershell.exe", "-NoProfile", "-STA", "-Command", `[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false); Add-Type -AssemblyName System.Windows.Forms; $d=New-Object System.Windows.Forms.FolderBrowserDialog; $d.Description='Select MCPX project folder'; $d.ShowNewFolderButton=$false; if($d.ShowDialog() -eq 'OK') { @{path=$d.SelectedPath} | ConvertTo-Json -Compress } else { '{"cancelled":true}' }; $d.Dispose()`}, nil
	case "linux":
		return []string{"zenity", "--file-selection", "--directory", "--title=Select MCPX project folder"}, nil
	}
	return nil, errors.New("this host has no supported native folder picker")
}
func SelectDirectory(ctx context.Context) (string, error) {
	if !dialogMu.TryLock() {
		return "", errors.New("已有文件夹选择器打开，请先完成或取消")
	}
	defer dialogMu.Unlock()
	argv, err := DirectoryCommand(runtime.GOOS)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.WaitDelay = time.Second
	var stderr strings.Builder
	command.Stderr = &stderr
	raw, err := command.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("原生选择器已关闭：%w", ctx.Err())
	}
	if err != nil {
		if runtime.GOOS == "linux" {
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1 {
				return "", ErrCancelled
			}
		}
		diagnostic := strings.TrimSpace(stderr.String())
		if len(diagnostic) > 1200 {
			diagnostic = diagnostic[:1200]
		}
		return "", fmt.Errorf("无法打开运行 MCPX 的电脑上的原生选择器（需要桌面登录会话）：%w %s", err, diagnostic)
	}
	if runtime.GOOS == "linux" {
		return strings.TrimSuffix(string(raw), "\n"), nil
	}
	var selection struct {
		Path      string `json:"path"`
		Cancelled bool   `json:"cancelled"`
	}
	if err = json.Unmarshal(raw, &selection); err != nil {
		return "", errors.New("原生选择器返回了无法识别的结果")
	}
	if selection.Cancelled {
		return "", ErrCancelled
	}
	if selection.Path == "" {
		return "", errors.New("没有选择项目目录")
	}
	return selection.Path, nil
}

type Info struct {
	Platform         string `json:"platform"`
	Executable       string `json:"executable"`
	PickerAvailable  bool   `json:"picker_available"`
	Signing          string `json:"signing"`
	PermissionStatus string `json:"permission_status"`
}

// Inspect never reads TCC.db or probes protected folders, which itself can
// trigger authorization. A remembered UI preference is not proof of a grant.
func Inspect(ctx context.Context) Info {
	path, _ := os.Executable()
	info := Info{Platform: runtime.GOOS, Executable: path, Signing: "not_applicable", PermissionStatus: "system_managed"}
	if argv, err := DirectoryCommand(runtime.GOOS); err == nil {
		_, err = exec.LookPath(argv[0])
		info.PickerAvailable = err == nil
	}
	if runtime.GOOS == "darwin" {
		info.Signing = "unknown"
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "/usr/bin/codesign", "-d", "-r-", "--verbose=2", path).CombinedOutput()
		if err == nil {
			text := string(out)
			if strings.Contains(text, "Signature=adhoc") || strings.Contains(text, "designated => cdhash") {
				info.Signing = "adhoc"
			} else if strings.Contains(text, "Authority=") {
				info.Signing = "certificate"
			}
		}
	}
	return info
}
func OpenPrivacy(ctx context.Context, pane string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("此入口仅用于 macOS 系统设置")
	}
	panes := map[string]string{"files": "Privacy_FilesAndFolders", "disk": "Privacy_AllFiles", "accessibility": "Privacy_Accessibility", "screen": "Privacy_ScreenCapture"}
	name, ok := panes[pane]
	if !ok {
		return errors.New("未知权限类别")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/bin/open", "x-apple.systempreferences:com.apple.preference.security?"+name).Run()
}
