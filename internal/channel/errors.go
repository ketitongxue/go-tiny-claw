package channel

import "errors"

// PermanentError marks a configuration/account failure that retry cannot fix.
// Other Start errors (including an unexpected nil return) are retryable.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

func Permanent(err error) error {
	if err == nil || IsPermanent(err) {
		return err
	}
	return &PermanentError{Err: err}
}

func IsPermanent(err error) bool {
	var permanent *PermanentError
	return errors.As(err, &permanent)
}
