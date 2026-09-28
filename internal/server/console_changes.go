package server

import (
	"context"
	"net/http"
	"time"

	"mcpx/internal/observation"
)

// changes is an on-demand read model. Commands may write files without going
// through apply_patch, so the console must inspect the current Git tree too.
func (c *consoleHandler) changes(w http.ResponseWriter, r *http.Request) {
	workspace, session := r.URL.Query().Get("workspace"), r.URL.Query().Get("session_id")
	if err := c.target(r.Context(), workspace, session); err != nil {
		consoleError(w, http.StatusNotFound, err.Error())
		return
	}
	registered, ok := c.runtime.reg.Get(workspace)
	if !ok {
		consoleError(w, http.StatusNotFound, "workspace not found")
		return
	}
	root := registered.Path
	if session != "" {
		if err := c.runtime.state.DB().QueryRowContext(r.Context(),
			`SELECT workspace_path FROM remote_sessions WHERE id=? AND workspace_name=?`, session, workspace).Scan(&root); err != nil {
			consoleError(w, http.StatusNotFound, "session no longer available")
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	report, err := c.runtime.workspaceDiff.Inspect(ctx, session, workspace, root, true)
	if err != nil {
		consoleError(w, http.StatusInternalServerError, "cannot inspect workspace changes")
		return
	}
	report.UnifiedDiff = observation.RedactText(report.UnifiedDiff)
	consoleJSON(w, http.StatusOK, report)
}
