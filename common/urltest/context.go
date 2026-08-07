package urltest

import "context"

type contextKeyIsUnifiedDelay struct{}

// ContextWithIsUnifiedDelay marks a context as requesting unified delay: URLTest
// then reports the RTT of a second request over the already established
// connection, excluding the TCP/TLS handshake of the first one.
func ContextWithIsUnifiedDelay(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextKeyIsUnifiedDelay{}, true)
}

func IsUnifiedDelayFromContext(ctx context.Context) bool {
	return ctx.Value(contextKeyIsUnifiedDelay{}) != nil
}
