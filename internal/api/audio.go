package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gabriel-vasile/mimetype"
	"github.com/gin-gonic/gin"

	"voice-notes/internal/domain"
)

// audioField is the multipart form field that carries the upload.
const audioField = "audio"

// supportedAudio maps a content-sniffed type (matched including aliases) to
// the MIME type sent to the model. The client-supplied Content-Type is never
// trusted.
var supportedAudio = []struct{ detected, send string }{
	{"audio/wav", "audio/wav"},
	{"audio/mpeg", "audio/mp3"},
	{"audio/aiff", "audio/aiff"},
	{"audio/aac", "audio/aac"},
	{"audio/ogg", "audio/ogg"},
	{"audio/flac", "audio/flac"},
	{"audio/x-m4a", "audio/mp4"}, // iOS Voice Memos default
	{"audio/mp4", "audio/mp4"},
	{"audio/webm", "audio/webm"}, // browser MediaRecorder; detected as video/webm
}

// detectAudio sniffs the content type from the file's bytes.
func detectAudio(data []byte) (sendAs, detected string, ok bool) {
	mt := mimetype.Detect(data)
	for _, s := range supportedAudio {
		if mt.Is(s.detected) {
			return s.send, mt.String(), true
		}
	}
	return "", mt.String(), false
}

// readAudio reads and validates the uploaded file. The request body should
// already be capped with http.MaxBytesReader. Errors are apiError values.
func readAudio(c *gin.Context, maxBytes int64) (domain.Audio, error) {
	tooLarge := apiError{http.StatusRequestEntityTooLarge, "audio_too_large",
		fmt.Sprintf("The audio file is too large. The limit is %d MB.", maxBytes>>20), 0}

	fh, err := c.FormFile(audioField)
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		return domain.Audio{}, tooLarge
	}
	if err != nil {
		return domain.Audio{}, errAudioMissing
	}
	if fh.Size > maxBytes {
		return domain.Audio{}, tooLarge
	}

	f, err := fh.Open()
	if err != nil {
		return domain.Audio{}, errUnreadableUpload
	}
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return domain.Audio{}, errUnreadableUpload
	}
	if len(data) == 0 {
		return domain.Audio{}, errAudioEmpty
	}

	mime, detected, ok := detectAudio(data)
	if !ok {
		return domain.Audio{}, apiError{http.StatusUnsupportedMediaType, "unsupported_format",
			fmt.Sprintf("Unsupported file type (%s). Use WAV, MP3, M4A, AAC, OGG, FLAC or WebM audio.", detected), 0}
	}
	return domain.Audio{Data: data, MIMEType: mime}, nil
}
