package logging

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"
)

// Log writes a structured log line: timestamp level msg key=val ...
func Log(level, msg string, keyvals ...interface{}) {
	parts := []string{
		time.Now().Format(time.RFC3339),
		strings.ToUpper(level),
		msg,
	}
	for i := 0; i < len(keyvals)-1; i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", keyvals[i], keyvals[i+1]))
	}
	log.Println(strings.Join(parts, " "))
}

// Info logs at info level
func Info(msg string, keyvals ...interface{}) {
	Log("info", msg, keyvals...)
}

// Error logs at error level
func Error(msg string, keyvals ...interface{}) {
	Log("error", msg, keyvals...)
}

// Verbose returns true if DEVUP_VERBOSE=1
func Verbose() bool {
	return os.Getenv("DEVUP_VERBOSE") == "1"
}
