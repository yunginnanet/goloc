package loc

import (
	"os"
	"runtime"

	"github.com/rs/zerolog"
)

func init() {
	cLogger := &zerolog.ConsoleWriter{
		Out:        os.Stderr,
		NoColor:    runtime.GOOS == "windows",
		TimeFormat: zerolog.TimeFormatUnix,
	}
	logger := zerolog.New(cLogger).With().Timestamp().Logger()
	Logger = &logger
}
