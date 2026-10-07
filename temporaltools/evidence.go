package temporaltools

import (
	"context"

	"github.com/achirothmane/governed-agent-runtime/mcptransport"
)

type EvidenceSink interface {
	Called(context.Context, InvocationRef) error
	Returned(context.Context, InvocationRef, mcptransport.Result, string, bool) error
	Failed(context.Context, InvocationRef, string) error
}
