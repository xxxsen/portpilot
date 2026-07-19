package cli

import "errors"

type UsageError struct {
	err error
}

func (e UsageError) Error() string {
	return e.err.Error()
}

func (e UsageError) Unwrap() error {
	return e.err
}

func usageError(err error) error {
	return UsageError{err: err}
}

func NewUsageError(err error) error {
	return usageError(err)
}

func IsUsageError(err error) bool {
	var target UsageError
	return errors.As(err, &target)
}
