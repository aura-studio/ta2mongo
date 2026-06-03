// package logging is the shared logging foundation for the whole module. Instead of
// threading a *logrus.Logger through every constructor, all packages call the
// package-level helpers here, which delegate to a single process-wide logger.
//
// Configure it once at startup with Init; everything logged afterwards uses the
// chosen level and format.
package logging

import (
	"strings"

	"github.com/sirupsen/logrus"
)

// std is the single process-wide logger. It is ready to use before Init is
// called (text formatter, info level) so library code and tests never see a nil
// logger.
var std = newDefault()

func newDefault() *logrus.Logger {
	l := logrus.New()
	l.SetFormatter(&logrus.TextFormatter{FullTimestamp: true})
	l.SetLevel(logrus.InfoLevel)
	return l
}

// Fields is a set of structured log fields, aliasing logrus.Fields so callers
// can build field maps without importing logrus directly.
type Fields = logrus.Fields

// Init sets the shared logger's level from a string (e.g. "debug", "info").
// An unparseable level falls back to info. Call once during startup.
func Init(level string) {
	lvl, err := logrus.ParseLevel(strings.ToLower(level))
	if err != nil {
		lvl = logrus.InfoLevel
	}
	std.SetLevel(lvl)
}

// L returns the shared logger for the rare caller that needs the *logrus.Logger
// itself (e.g. to hand to a third-party API). Prefer the package-level helpers.
func L() *logrus.Logger { return std }

// WithError starts a log entry carrying err.
func WithError(err error) *logrus.Entry { return std.WithError(err) }

// WithField starts a log entry carrying a single structured field.
func WithField(key string, value any) *logrus.Entry { return std.WithField(key, value) }

// WithFields starts a log entry carrying multiple structured fields.
func WithFields(fields Fields) *logrus.Entry { return std.WithFields(fields) }

// Info logs at info level.
func Info(args ...any) { std.Info(args...) }

// Infof logs a formatted message at info level.
func Infof(format string, args ...any) { std.Infof(format, args...) }

// Warn logs at warn level.
func Warn(args ...any) { std.Warn(args...) }

// Warnf logs a formatted message at warn level.
func Warnf(format string, args ...any) { std.Warnf(format, args...) }

// Error logs at error level.
func Error(args ...any) { std.Error(args...) }

// Errorf logs a formatted message at error level.
func Errorf(format string, args ...any) { std.Errorf(format, args...) }

// Debug logs at debug level.
func Debug(args ...any) { std.Debug(args...) }

// Debugf logs a formatted message at debug level.
func Debugf(format string, args ...any) { std.Debugf(format, args...) }
