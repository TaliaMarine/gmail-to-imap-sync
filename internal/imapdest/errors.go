package imapdest

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/emersion/go-imap/v2"
)

// Kind classifies a destination error for the failure table (§7.3).
type Kind string

const (
	KindAuth       Kind = "imap_auth"
	KindConnection Kind = "imap_connection"
	KindFolder     Kind = "imap_folder"
	KindAppend     Kind = "imap_append"
	KindParse      Kind = "parse"
)

// OpError is an error from the destination, tagged with what failed.
type OpError struct {
	Kind   Kind
	Op     string
	Folder string
	Err    error
}

func (e *OpError) Error() string {
	s := string(e.Kind) + ": " + e.Op
	if e.Folder != "" {
		s += " " + e.Folder
	}
	return s + ": " + e.Err.Error()
}

func (e *OpError) Unwrap() error { return e.Err }

// KindOf returns the Kind of err, or KindConnection for connection-level
// errors that were not wrapped.
func KindOf(err error) Kind {
	var oe *OpError
	if errors.As(err, &oe) {
		return oe.Kind
	}
	if errors.Is(err, ErrMalformed) {
		return KindParse
	}
	if isConnectionError(err) {
		return KindConnection
	}
	return KindAppend
}

// Retryable reports whether trying again later can succeed. Authentication
// and folder-permission problems need an administrator.
func Retryable(err error) bool {
	switch KindOf(err) {
	case KindAuth, KindParse:
		return false
	}
	var ie *imap.Error
	if errors.As(err, &ie) {
		switch ie.Code {
		case imap.ResponseCodeNoPerm, imap.ResponseCodeAuthorizationFailed, imap.ResponseCodeTooBig:
			return false
		}
	}
	return true
}

func isConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var ne net.Error
	var ie *imap.Error
	switch {
	case errors.As(err, &ie):
		// A status response from the server is not a broken connection,
		// except BYE.
		return ie.Type == imap.StatusResponseTypeBye
	case errors.As(err, &ne), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF),
		errors.Is(err, net.ErrClosed), errors.Is(err, errBroken):
		return true
	}
	return false
}

var errBroken = errors.New("imap connection closed")

func isCtxErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
