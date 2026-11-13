// Package apperr defines ytx's named error codes, shared by the JSON output and
// the exit codes. Agents switch on these codes, so never rename one.
package apperr

import (
	"context"
	"errors"
	"fmt"
	"net"
)

const (
	InvalidInput     = "invalid_input"
	NotFound         = "not_found"
	NotImplemented   = "not_implemented"
	QuotaExceeded    = "quota_exceeded"    // the official API's daily units are used up
	NeedsAPIKey      = "needs_api_key"     // the command only works with YOUTUBE_API_KEY
	LoginRequired    = "login_required"    // the command needs `ytx login` (OAuth)
	ApprovalRequired = "approval_required" // a write ran without --yes and no terminal to ask
	Declined         = "declined"          // the person answered no to a write
	Forbidden        = "forbidden"         // YouTube refused (not your video, private, disabled)
	CommentsDisabled = "comments_disabled"
	NoTranscript     = "no_transcript"
	YouTubeError     = "youtube_error" // YouTube answered with something ytx couldn't read
	ToolMissing      = "tool_missing"  // an external tool (yt-dlp, ffmpeg) is not installed
	NetworkError     = "network_error"
	Timeout          = "timeout"
	Internal         = "internal_error"
)

// ExitCodes groups codes by exit status, so scripts can branch without parsing JSON.
var ExitCodes = map[int][]string{
	1: {Internal, YouTubeError, NotImplemented},
	2: {InvalidInput},
	3: {NotFound, NoTranscript, CommentsDisabled},
	4: {LoginRequired, NeedsAPIKey, Forbidden},
	5: {QuotaExceeded},
	6: {ApprovalRequired, Declined},
	7: {NetworkError, Timeout},
	8: {ToolMissing},
}

// ExitCode is the process exit status for a code.
func ExitCode(code string) int {
	for n, codes := range ExitCodes {
		for _, c := range codes {
			if c == code {
				return n
			}
		}
	}
	return 1
}

// Error is a coded error. Detail is optional structured data (e.g. the planned
// action of a write that needs approval).
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Detail  any    `json:"detail,omitempty"`
	Err     error  `json:"-"`
}

func (e *Error) Error() string { return e.Message }
func (e *Error) Unwrap() error { return e.Err }

// New makes a coded error.
func New(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Wrap makes a coded error that keeps the cause.
func Wrap(code string, err error, format string, args ...any) *Error {
	msg := fmt.Sprintf(format, args...)
	if err != nil {
		msg += ": " + err.Error()
	}
	return &Error{Code: code, Message: msg, Err: err}
}

// From turns any error into a coded one.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Wrap(Timeout, err, "timed out")
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return Wrap(NetworkError, err, "network error")
	}
	return &Error{Code: Internal, Message: err.Error(), Err: err}
}
