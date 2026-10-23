package postgres

// The database keeps unknown as a compatibility value for observations that
// genuinely cannot be concluded. Newly created resources and read projections
// use explicit lifecycle values so an uninitialized row is not shown as a
// missing status.
func normalizeRuntimePhase(phase string) string {
	if phase == "" || phase == "unknown" {
		return "pending"
	}
	return phase
}

func normalizePublicationPhase(phase string) string {
	if phase == "" || phase == "unknown" {
		return "withdrawn"
	}
	return phase
}

func normalizeInvocationHealth(runtimePhase, publicationPhase, health string) string {
	if health == "" {
		health = "unhealthy"
	}
	if health == "unknown" && (runtimePhase != "ready" || publicationPhase != "published") {
		return "unhealthy"
	}
	return health
}
