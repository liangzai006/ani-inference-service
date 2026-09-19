package inference

import "context"

// ModelReferenceReader covers every retained spec of an undeleted service,
// including pending, stopped, failed and deleting services.
type ModelReferenceReader interface {
	HasActiveModelVersionReferences(context.Context, string, []string) (bool, error)
}
