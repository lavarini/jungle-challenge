package platform

import (
	"io"
	"log/slog"
)

// NewLogger writes JSON logs. Callers add correlationId, messageId,
// transactionId, walletId and providerId as attributes when available.
func NewLogger(w io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
}
