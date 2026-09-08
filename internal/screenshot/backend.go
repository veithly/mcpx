package screenshot

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"mcpx/internal/winproc"
)

func captureNative(parent context.Context, request Request, outputPath string) error {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		binary, err := exec.LookPath("screencapture")
		if err != nil {
			return fmt.Errorf("macOS screencapture is unavailable")
		}
		args := []string{"-x", "-t", "png"}
		if request.Mode == "region" {
			args = append(args, "-R", fmt.Sprintf("%d,%d,%d,%d", request.X, request.Y, request.Width, request.Height))
		} else {
			args = append(args, "-D", strconv.Itoa(request.Display+1))
		}
		command = exec.CommandContext(ctx, binary, append(args, outputPath)...)
	case "linux":
		command = linuxCaptureCommand(ctx, request, outputPath)
		if command == nil {
			return fmt.Errorf("no Linux screenshot backend found; install grim, scrot, or ImageMagick")
		}
	case "windows":
		bounds, err := captureBounds(parent, request)
		if err != nil {
			return err
		}
		script := fmt.Sprintf(`Add-Type -AssemblyName System.Drawing; $b=New-Object Drawing.Bitmap %d,%d; $g=[Drawing.Graphics]::FromImage($b); $g.CopyFromScreen(%d,%d,0,0,$b.Size); $b.Save('%s',[Drawing.Imaging.ImageFormat]::Png); $g.Dispose(); $b.Dispose()`,
			bounds.Width, bounds.Height, bounds.X, bounds.Y, strings.ReplaceAll(outputPath, "'", "''"))
		command = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	default:
		return fmt.Errorf("screenshots are not supported on %s", runtime.GOOS)
	}
	// Windows 下隐藏截图命令创建的控制台窗口；其他系统为空操作。
	winproc.ConfigureNoWindow(command)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("screen capture failed: %s; grant Screen Recording to the mcpx-server binary (identifier com.mcpx.server) in System Settings → Privacy → Screen Recording — the operator workbench native-permissions panel links there — then retry without changing the request", message)
	}
	return nil
}

func linuxCaptureCommand(ctx context.Context, request Request, outputPath string) *exec.Cmd {
	bounds, boundsErr := captureBounds(ctx, request)
	if binary, err := exec.LookPath("grim"); err == nil {
		args := []string{}
		if request.Mode == "region" || boundsErr == nil && request.Display > 0 {
			args = append(args, "-g", fmt.Sprintf("%d,%d %dx%d", bounds.X, bounds.Y, bounds.Width, bounds.Height))
		}
		return exec.CommandContext(ctx, binary, append(args, outputPath)...)
	}
	if binary, err := exec.LookPath("scrot"); err == nil {
		args := []string{"--overwrite"}
		if request.Mode == "region" || boundsErr == nil && request.Display > 0 {
			args = append(args, "-a", fmt.Sprintf("%d,%d,%d,%d", bounds.X, bounds.Y, bounds.Width, bounds.Height))
		}
		return exec.CommandContext(ctx, binary, append(args, outputPath)...)
	}
	if binary, err := exec.LookPath("import"); err == nil {
		args := []string{"-window", "root"}
		if request.Mode == "region" || boundsErr == nil && request.Display > 0 {
			args = append(args, "-crop", fmt.Sprintf("%dx%d+%d+%d", bounds.Width, bounds.Height, bounds.X, bounds.Y))
		}
		return exec.CommandContext(ctx, binary, append(args, outputPath)...)
	}
	return nil
}

func captureBounds(ctx context.Context, request Request) (Display, error) {
	if request.Mode == "region" {
		return Display{X: request.X, Y: request.Y, Width: request.Width, Height: request.Height}, nil
	}
	displays := Displays(ctx)
	if request.Display >= len(displays) {
		return Display{}, fmt.Errorf("display %d not found; %d displays detected", request.Display, len(displays))
	}
	if len(displays) > 1 && !displays[request.Display].PositionKnown {
		return Display{}, fmt.Errorf("display %d position is unavailable; use fullscreen on display 0 or provide explicit region coordinates", request.Display)
	}
	return displays[request.Display], nil
}
