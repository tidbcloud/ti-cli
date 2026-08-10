package api

import (
	"fmt"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

type Error struct {
	Code       string
	Category   string
	ExitCode   int
	StatusCode int
	Message    string
	Body       string
	Cause      error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Cause)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func (e *Error) AppError() *apperr.Error {
	if e == nil {
		return nil
	}
	return apperr.Wrap(e.Code, e.Category, e.ExitCode, e.Message, e.Cause)
}
