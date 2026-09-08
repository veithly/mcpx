package server

import (
	"errors"
	"net/http"

	"mcpx/internal/nativeui"
)

func (c *consoleHandler) nativeInfo(w http.ResponseWriter, r *http.Request) {
	consoleJSON(w, 200, nativeui.Inspect(r.Context()))
}
func (c *consoleHandler) chooseNativeFolder(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm bool `json:"confirm"`
	}
	if err := consoleDecode(w, r, &input); err != nil || !input.Confirm {
		consoleError(w, 400, "请点击选择文件夹主动打开原生对话框")
		return
	}
	picker := c.nativePicker
	if picker == nil {
		picker = nativeui.SelectDirectory
	}
	path, err := picker(r.Context())
	if errors.Is(err, nativeui.ErrCancelled) {
		consoleJSON(w, 200, map[string]any{"cancelled": true})
		return
	}
	if err != nil {
		consoleError(w, 503, err.Error())
		return
	}
	if err := validateSelectedDirectory(path); err != nil {
		consoleError(w, 500, err.Error())
		return
	}
	consoleJSON(w, 200, map[string]any{"path": path, "cancelled": false})
}
func (c *consoleHandler) openPrivacy(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Pane    string `json:"pane"`
		Confirm bool   `json:"confirm"`
	}
	if err := consoleDecode(w, r, &input); err != nil || !input.Confirm {
		consoleError(w, 400, "请主动选择要打开的系统权限设置")
		return
	}
	if err := nativeui.OpenPrivacy(r.Context(), input.Pane); err != nil {
		consoleError(w, 400, err.Error())
		return
	}
	consoleJSON(w, 200, map[string]any{"opened": true, "permission_status": "system_managed"})
}
