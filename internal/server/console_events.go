package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"mcpx/internal/observation"
)

func (c *consoleHandler) events(w http.ResponseWriter, r *http.Request) {
	ws, session := r.URL.Query().Get("workspace"), r.URL.Query().Get("session_id")
	if err := c.target(r.Context(), ws, session); err != nil {
		consoleError(w, 404, err.Error())
		return
	}
	events, cursor, err := c.runtime.observation.store.Query(r.Context(), observation.HistoryQuery{Workspace: ws, SessionID: session, Cursor: r.URL.Query().Get("cursor"), Limit: 100})
	if err != nil {
		consoleError(w, 400, "cannot read event history or invalid cursor")
		return
	}
	if events == nil {
		events = []observation.Event{}
	}
	var last int64
	if len(events) > 0 {
		last = events[0].Sequence
	}
	for i := range events {
		boundConsoleEvent(&events[i])
	}
	consoleJSON(w, 200, map[string]any{"events": events, "next_cursor": cursor, "last_sequence": last})
}
func boundConsoleEvent(e *observation.Event) {
	if len(e.Input) > 16384 {
		e.Input, _ = json.Marshal(map[string]any{"truncated": true, "note": "Large input omitted from console timeline."})
		e.Truncated = true
	}
	if len(e.Output) > 32768 {
		e.Output, _ = json.Marshal(map[string]any{"truncated": true, "note": "Large output omitted. Open execution logs or use observe for the full artifact."})
		e.Truncated = true
	}
	e.Summary, _ = observation.SanitizeText(e.Summary, 16384)
}

func (c *consoleHandler) stream(w http.ResponseWriter, r *http.Request) {
	ws, session := r.URL.Query().Get("workspace"), r.URL.Query().Get("session_id")
	if err := c.target(r.Context(), ws, session); err != nil {
		consoleError(w, 404, err.Error())
		return
	}
	after := int64(0)
	for _, raw := range []string{r.URL.Query().Get("after"), r.Header.Get("Last-Event-ID")} {
		if raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 {
				consoleError(w, 400, "invalid stream cursor")
				return
			}
			if n > after {
				after = n
			}
		}
	}
	c.mu.Lock()
	if c.streams >= 32 {
		c.mu.Unlock()
		consoleError(w, 429, "too many event streams")
		return
	}
	c.streams++
	c.mu.Unlock()
	defer func() { c.mu.Lock(); c.streams--; c.mu.Unlock() }()
	// Subscribe before replay so commits during the initial query cannot be lost.
	sub := c.runtime.observation.broker.Subscribe(ws, 128)
	defer sub.Close()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func(kind string, id int64, data any) bool {
		_ = controller.SetWriteDeadline(time.Now().Add(20 * time.Second))
		raw, err := json.Marshal(data)
		if err != nil {
			return false
		}
		if id > 0 {
			if _, err = fmt.Fprintf(w, "id: %d\n", id); err != nil {
				return false
			}
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	catchup := func() bool {
		for {
			events, _, err := c.runtime.observation.store.Query(r.Context(), observation.HistoryQuery{Workspace: ws, SessionID: session, AfterSequence: after, Ascending: true, Limit: 200})
			if err != nil {
				return false
			}
			for _, event := range events {
				if event.Sequence <= after {
					continue
				}
				boundConsoleEvent(&event)
				if !send("activity", event.Sequence, event) {
					return false
				}
				after = event.Sequence
			}
			if len(events) < 200 {
				return true
			}
		}
	}
	if !send("ready", 0, map[string]any{"after": after}) || !catchup() {
		return
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-sub.Events:
			if !ok {
				return
			}
			if session == "" || event.RemoteSessionID == session {
				if !catchup() {
					return
				}
			}
			if event.RemoteSessionID == "" {
				if !send("refresh", 0, map[string]string{"workspace": ws}) {
					return
				}
			}
		case _, ok := <-sub.Gaps:
			if !ok {
				return
			}
			if !catchup() {
				return
			}
		case <-ticker.C:
			if _, ok := c.authorized(r); !ok {
				_ = send("expired", 0, map[string]bool{"authenticated": false})
				return
			}
			if !catchup() || !send("heartbeat", 0, map[string]any{"after": after}) {
				return
			}
		}
	}
}

func (c *consoleHandler) logs(w http.ResponseWriter, r *http.Request) {
	ws, session := r.URL.Query().Get("workspace"), r.URL.Query().Get("session_id")
	if session == "" {
		consoleError(w, 400, "session_id required")
		return
	}
	if err := c.target(r.Context(), ws, session); err != nil {
		consoleError(w, 404, err.Error())
		return
	}
	task, err := c.runtime.tasks.Get(session, r.URL.Query().Get("execution_task_id"))
	if err != nil {
		consoleError(w, 404, "task not found")
		return
	}
	stream := r.URL.Query().Get("stream")
	if stream == "" {
		stream = "combined"
	}
	if stream != "combined" && stream != "stdout" && stream != "stderr" {
		consoleError(w, 400, "invalid log stream")
		return
	}
	offset := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			consoleError(w, 400, "invalid log offset")
			return
		}
	} else {
		size := task.LogStreamSize(stream)
		if size > 65536 {
			offset = int(size - 65536)
		}
	}
	chunk, next := task.LogsFor(stream, offset)
	if len(chunk) > 65536 {
		chunk = chunk[:65536]
		next = offset + len(chunk)
	}
	chunk, truncated := observation.SanitizeText(chunk, 65536)
	view := task.StatusView()
	if command, ok := view["command"].(string); ok {
		view["command"] = observation.SanitizeIntent(command)
	}
	consoleJSON(w, 200, map[string]any{"text": chunk, "offset": offset, "next_offset": next, "truncated": truncated, "task": view})
}
