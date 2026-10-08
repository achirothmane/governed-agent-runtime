package agentloop

import "errors"

// ErrInvalidReasoningContext marks deterministic local/context failures that
// retrying the model cannot repair (for example missing bound tool schemas or
// an input that exceeds the configured provider context boundary).
var ErrInvalidReasoningContext = errors.New("invalid agent reasoning context")
