package execution

func containsEnv(env []string, want string) bool {
	for _, item := range env {
		if item == want {
			return true
		}
	}
	return false
}

func containsReason(reasoning []string, want string) bool {
	for _, reason := range reasoning {
		if reason == want {
			return true
		}
	}
	return false
}
