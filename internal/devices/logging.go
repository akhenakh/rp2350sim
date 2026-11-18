package devices

import "log/slog"

// discardLogger is shared by devices that were not given a logger.
var discardLogger = slog.New(slog.DiscardHandler)

// loggerOr returns l, or a discarding logger when the device is silent.
func loggerOr(l *slog.Logger) *slog.Logger {
	if l == nil {
		return discardLogger
	}
	return l
}
