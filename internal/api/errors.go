package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"voice-notes/internal/domain"
)

// apiError is a client-facing failure. Its message is a friendly sentence that
// is safe to show to users; it never contains provider or vendor internals,
// which go to the logs instead.
type apiError struct {
	status     int
	code       string // stable snake_case identifier for clients
	message    string
	retryAfter int // seconds; 0 means "don't retry as-is"
}

func (e apiError) Error() string   { return e.code + ": " + e.message }
func (e apiError) retryable() bool { return e.retryAfter > 0 }

// errorResponse is the body of every error response, from the handler and the
// middleware alike, so clients parse a single shape.
type errorResponse struct {
	RequestID string `json:"request_id"`
	Error     string `json:"error"`
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}

// writeError ends the request with e, setting Retry-After when it's worth retrying.
func writeError(c *gin.Context, e apiError) {
	if e.retryable() {
		c.Header("Retry-After", strconv.Itoa(e.retryAfter))
	}
	c.AbortWithStatusJSON(e.status, errorResponse{
		RequestID: c.GetString(requestIDKey),
		Error:     e.message,
		Code:      e.code,
		Retryable: e.retryable(),
	})
}

// asAPIError returns err as an apiError, or a generic one for anything
// unexpected, so a programming mistake can never leak internals to clients.
func asAPIError(err error) apiError {
	if e, ok := errors.AsType[apiError](err); ok {
		return e
	}
	return errInternal
}

// Request and load errors.
var (
	errAudioMissing = apiError{http.StatusBadRequest, "audio_missing",
		`No audio received. Send the file in a multipart form field named "audio".`, 0}
	errAudioEmpty = apiError{http.StatusBadRequest, "audio_empty",
		"The audio file is empty.", 0}
	errUnreadableUpload = apiError{http.StatusBadRequest, "unreadable_upload",
		"The uploaded file could not be read. Please try again.", 0}
	errUnauthorized = apiError{http.StatusUnauthorized, "unauthorized",
		"Missing or invalid API key.", 0}
	errRateLimited = apiError{http.StatusTooManyRequests, "rate_limited",
		"Too many requests. Please wait a few seconds and try again.", 10}
	errNotFound = apiError{http.StatusNotFound, "not_found",
		"There is nothing at this address.", 0}
	errInternal = apiError{http.StatusInternalServerError, "internal_error",
		"Something went wrong on our side. If it keeps happening, share the request ID.", 0}
	errServerBusy = apiError{http.StatusServiceUnavailable, "server_busy",
		"The server is busy processing other memos. Please try again in a few seconds.", 5}
)

// Analysis errors, selected by analyzeError.
var (
	errNoSpeech = apiError{http.StatusUnprocessableEntity, "no_speech",
		"We couldn't hear any speech in this recording. Try again, a bit closer to the microphone.", 0}
	errModelBusy = apiError{http.StatusServiceUnavailable, "model_busy",
		"The AI model is overloaded right now (a temporary issue on Google's side). Please try again in a few seconds.", 10}
	errModelQuota = apiError{http.StatusTooManyRequests, "model_quota_exceeded",
		"The AI usage limit for this server has been reached. Please wait a minute and try again.", 60}
	errModelTimeout = apiError{http.StatusGatewayTimeout, "model_timeout",
		"The AI took too long to respond. Please try again.", 5}
	errAnalysisFailed = apiError{http.StatusBadGateway, "analysis_failed",
		"Something went wrong while analyzing the audio. If it keeps happening, share the request ID.", 0}
)

// analyzeError maps analyzer failures, expressed as provider-neutral domain
// errors, to client responses.
func analyzeError(err error) apiError {
	switch {
	case errors.Is(err, domain.ErrNoSpeech):
		return errNoSpeech
	case errors.Is(err, domain.ErrModelBusy):
		return errModelBusy
	case errors.Is(err, domain.ErrQuotaExceeded):
		return errModelQuota
	case errors.Is(err, context.DeadlineExceeded):
		return errModelTimeout
	default:
		return errAnalysisFailed
	}
}
