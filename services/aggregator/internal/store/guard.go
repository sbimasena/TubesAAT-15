package store

import "context"

type writeVerifierKey struct{}

// WithWriteVerifier rechecks the term before background store mutations. Storage
// epochs still reject races between verification and a unit transaction.
func WithWriteVerifier(ctx context.Context, verify func(context.Context) error) context.Context {
	return context.WithValue(ctx, writeVerifierKey{}, verify)
}
func verifyWrite(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if verify, ok := ctx.Value(writeVerifierKey{}).(func(context.Context) error); ok {
		return verify(ctx)
	}
	return nil
}
