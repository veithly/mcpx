package server

func nextAction(tool string, arguments map[string]any) map[string]any {
	return nextActionWithReason(tool, "continue with the returned operation", arguments)
}

func boolPayload(payload map[string]any, key string) bool {
	value, _ := payload[key].(bool)
	return value
}
