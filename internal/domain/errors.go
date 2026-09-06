package domain

import (
	"errors"
	"fmt"
	"time"
)

// ErrNotFound is returned when a row does not exist, or exists but belongs to
// another tenant. The two are deliberately indistinguishable.
var ErrNotFound = errors.New("introuvable")

// ErrReauthorize is what a tool returns once LinkedIn rejects the token.
var ErrReauthorize = errors.New("autorisation expirée, utilisez reconnect_url")

// ErrForbiddenMember is returned when ALLOWED_MEMBER_IDS is set and the
// LinkedIn account that just logged in is not on the list.
var ErrForbiddenMember = errors.New("ce compte LinkedIn n'est pas autorisé sur ce serveur")

// ErrScopeMissing is returned when an operation needs a permission the member
// never granted, or that the app was never approved for.
type ErrScopeMissing struct {
	Scope string
	Why   string
}

func (e *ErrScopeMissing) Error() string {
	if e.Why != "" {
		return fmt.Sprintf("permission %s absente : %s", e.Scope, e.Why)
	}
	return fmt.Sprintf("permission %s absente", e.Scope)
}

// APIError is a decoded LinkedIn error response.
type APIError struct {
	HTTPStatus int
	// ServiceErrorCode is LinkedIn's own numeric code, when present.
	ServiceErrorCode int
	Code             string
	Message          string
	RetryAfter       time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("linkedin api: HTTP %d code=%s (%d): %s",
		e.HTTPStatus, e.Code, e.ServiceErrorCode, e.Message)
}

// IsAuth reports whether the token can no longer be used: expired, revoked,
// or missing a permission.
func (e *APIError) IsAuth() bool {
	return e.HTTPStatus == 401 || e.HTTPStatus == 403
}

// IsRateLimit reports whether LinkedIn is throttling us.
func (e *APIError) IsRateLimit() bool { return e.HTTPStatus == 429 }

// IsNotFound reports whether the object does not exist. LinkedIn answers 404
// for a post with no social activity at all, which is not an error.
func (e *APIError) IsNotFound() bool { return e.HTTPStatus == 404 }

// UserMessage turns an API error into the French sentence a tool returns.
func (e *APIError) UserMessage() string {
	switch {
	case e.HTTPStatus == 401:
		return ErrReauthorize.Error()
	case e.HTTPStatus == 403:
		return "LinkedIn a refusé l'accès : permission manquante sur l'application, ou vous n'êtes pas l'auteur de cet objet"
	case e.IsRateLimit():
		return "quota LinkedIn atteint, réessayez dans quelques minutes"
	case e.IsNotFound():
		return "objet introuvable chez LinkedIn"
	default:
		return fmt.Sprintf("erreur LinkedIn (HTTP %d) : %s", e.HTTPStatus, e.Message)
	}
}

// AsAPIError unwraps err into an *APIError when there is one.
func AsAPIError(err error) (*APIError, bool) {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae, true
	}
	return nil, false
}
