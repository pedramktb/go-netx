package netx_test

import (
	"context"
	"log"
	"sync"
)

// memLogger captures logs for assertion in tests
type memLogger struct {
	mu      sync.Mutex
	entries []string
}

func (l *memLogger) append(level, msg string) {
	l.mu.Lock()
	l.entries = append(l.entries, level+": "+msg)
	l.mu.Unlock()
}

func (l *memLogger) print(level, msg string, args ...any) {
	if len(args) > 0 {
		log.Printf("%s: %s %v", level, msg, args)
	} else {
		log.Printf("%s: %s", level, msg)
	}
}

func (l *memLogger) DebugContext(ctx context.Context, msg string, args ...any) {
	l.append("DEBUG", msg)
	l.print("DEBUG", msg, args...)
}
func (l *memLogger) InfoContext(ctx context.Context, msg string, args ...any) {
	l.append("INFO", msg)
	l.print("INFO", msg, args...)
}
func (l *memLogger) WarnContext(ctx context.Context, msg string, args ...any) {
	l.append("WARN", msg)
	l.print("WARN", msg, args...)
}
func (l *memLogger) ErrorContext(ctx context.Context, msg string, args ...any) {
	l.append("ERROR", msg)
	l.print("ERROR", msg, args...)
}
