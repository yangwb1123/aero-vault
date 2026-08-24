package thumbnail

import "errors"

// joinSourceCloseError keeps a source-close failure visible at the module
// boundary while preserving the primary decode error when both operations
// fail. SourceReadError is the existing adapter classification seam.
func joinSourceCloseError(primary, closeErr error) error {
	if closeErr == nil {
		return primary
	}
	sourceErr := &SourceReadError{Err: closeErr}
	if primary == nil {
		return sourceErr
	}
	return errors.Join(primary, sourceErr)
}
