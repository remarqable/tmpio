// Package logger configures the process-wide structured logger.
package logger

import (
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var logger zerolog.Logger

// Init configures zerolog. Development uses a console writer.
func Init(env string) {
	zerolog.TimeFieldFormat = time.RFC3339
	if env == "dev" || env == "test" {
		logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339}).
			With().Timestamp().Logger()
	} else {
		logger = zerolog.New(os.Stdout).With().Timestamp().Logger()
	}
	log.Logger = logger
}

// Get returns the process logger.
func Get() *zerolog.Logger { return &logger }
