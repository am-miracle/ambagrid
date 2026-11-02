package adapter

import "context"

type unsupportedFieldKey struct{}

// WithUnsupportedFieldHook observes unsupported registers even when an optional
// field is omitted and the rest of the reading succeeds.
func WithUnsupportedFieldHook(ctx context.Context, hook func()) context.Context {
	return context.WithValue(ctx, unsupportedFieldKey{}, hook)
}

func ReportUnsupportedField(ctx context.Context) {
	if hook, ok := ctx.Value(unsupportedFieldKey{}).(func()); ok {
		hook()
	}
}
