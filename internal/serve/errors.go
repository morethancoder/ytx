package serve

import (
	"context"
	"errors"
	"net/http"

	"github.com/morethancoder/ytx/internal/apperr"
)

// The codes the service answers with. Power's backend branches on these, so
// they never change and never carry YouTube's own words.
const (
	CodeNotFound    = "not_found"
	CodeCommentsOff = "comments_off"
	CodeUnavailable = "unavailable"
	CodeBadRequest  = "bad_request"
)

// codeError is an error that already is one of the service's codes.
type codeError struct{ code string }

func (e codeError) Error() string { return e.code }

func badRequest() error { return codeError{CodeBadRequest} }

// classify maps any error from the reader to a service code and an HTTP status.
// Anything it does not recognise is "unavailable": a blocked, changed or
// broken YouTube all mean the same to a caller, try again later.
func classify(err error) (code string, status int) {
	var ce codeError
	if errors.As(err, &ce) {
		return ce.code, statusOf(ce.code)
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case apperr.InvalidInput:
			return CodeBadRequest, http.StatusBadRequest
		case apperr.NotFound, apperr.Forbidden:
			// A private item looks the same as a missing one to a caller.
			return CodeNotFound, http.StatusNotFound
		case apperr.CommentsDisabled:
			return CodeCommentsOff, http.StatusUnprocessableEntity
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return CodeUnavailable, http.StatusGatewayTimeout
	}
	return CodeUnavailable, http.StatusServiceUnavailable
}

func statusOf(code string) int {
	switch code {
	case CodeBadRequest:
		return http.StatusBadRequest
	case CodeNotFound:
		return http.StatusNotFound
	case CodeCommentsOff:
		return http.StatusUnprocessableEntity
	}
	return http.StatusServiceUnavailable
}
