package thumbnail

import (
	"context"
	"io"
	"reflect"
)

// MagicPrefixLen is the fixed number of bytes needed to classify every image
// format admitted by AdmitByMagic. It is intentionally not configurable.
const MagicPrefixLen = 12

// ReadMagicPrefix reads one magic-classification prefix while checking the
// request context before every delegated read. It returns the bytes obtained
// even when the source ends early or a read fails, matching io.ReadFull's
// partial-read contract. The reader remains owned by the caller and is not
// closed here.
func ReadMagicPrefix(ctx context.Context, r io.Reader) ([]byte, error) {
	_ = ctx.Done() // preserve the package's nil-context caller-bug convention
	if isNilReader(r) {
		return nil, ErrNilSource
	}

	prefix := make([]byte, MagicPrefixLen)
	n, err := io.ReadFull(&ctxReader{ctx: ctx, r: r}, prefix)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return prefix[:n], ctxErr
	}
	return prefix[:n], err
}

func isNilReader(r io.Reader) bool {
	if r == nil {
		return true
	}
	value := reflect.ValueOf(r)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return value.IsNil()
	default:
		return false
	}
}
