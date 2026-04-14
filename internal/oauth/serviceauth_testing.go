package oauth

import "context"

// ContextWithActingDIDForTest injects a DID into the context as if the
// service-auth middleware had verified it. Exported for use from other
// packages' tests; the companion reader is ActingDIDFromContext.
//
// Name ends in ForTest per convention — this is not intended for prod
// code paths, only test scaffolding that exercises handlers downstream
// of the middleware.
func ContextWithActingDIDForTest(ctx context.Context, did string) context.Context {
	return context.WithValue(ctx, serviceAuthDIDKey, did)
}
