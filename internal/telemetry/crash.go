package telemetry

import (
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
)

// Crash reporting sends crashes, and only crashes, to Sentry beside SigNoz:
// a failed start, a serve loop that returns an error, and an uncaught panic.
// Handled errors stay in SigNoz to keep inside the free quota. Scope and
// limits: docs/telemetry.md.

const (
	// CrashEventsPerMinute caps each process so a crash loop cannot spend the
	// shared monthly quota.
	CrashEventsPerMinute = 20
	crashFlushTimeout    = 2 * time.Second
)

var (
	crashMu     sync.Mutex
	crashActive bool
	crashWindow []time.Time
)

// StartCrashReporting turns reporting on when SENTRY_DSN is set, tagging every
// event with the server name so each deployed guardfile stays separable in one
// project. It never fails a start: a bad DSN leaves reporting off and returns
// the error for the caller to log by type.
func StartCrashReporting(serverName string) (bool, error) {
	return startCrashReporting(strings.TrimSpace(os.Getenv("SENTRY_DSN")), serverName, nil)
}

func startCrashReporting(dsn, serverName string, transport sentry.Transport) (bool, error) {
	crashMu.Lock()
	defer crashMu.Unlock()
	crashActive = false
	if dsn == "" {
		return false, nil
	}
	environment := strings.TrimSpace(os.Getenv("OTEL_DEPLOYMENT_ENVIRONMENT"))
	if environment == "" {
		environment = "homelab"
	}
	if err := sentry.Init(sentry.ClientOptions{
		Dsn:           dsn,
		Environment:   environment,
		EnableTracing: false,
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			if !crashWithinBudget(time.Now()) {
				return nil
			}
			return event
		},
		Transport: transport,
	}); err != nil {
		return false, err
	}
	sentry.ConfigureScope(func(scope *sentry.Scope) {
		scope.SetTag("server.name", serverName)
	})
	crashActive = true
	return true, nil
}

// ReportCrash sends the error that is ending the process and waits for it,
// because the caller exits right after.
func ReportCrash(err error) {
	if err == nil || !crashReportingActive() {
		return
	}
	sentry.CaptureException(err)
	sentry.Flush(crashFlushTimeout)
}

// RecoverCrash reports a panic and re-panics, so the process still dies the
// way it would have. Use as `defer telemetry.RecoverCrash()`.
func RecoverCrash() {
	recovered := recover()
	if recovered == nil {
		return
	}
	reportPanic(recovered)
	panic(recovered)
}

// RecoverHandler reports a panic inside a request handler and re-panics, so
// net/http still logs it and drops the connection exactly as it did before.
func RecoverHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			// ErrAbortHandler is net/http's own deliberate abort, not a crash.
			if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				// The request rides on the event: method, path, and headers after the
				// SDK drops credentials. The body is never read.
				hub := sentry.CurrentHub().Clone()
				hub.Scope().SetRequest(r)
				reportPanicOn(hub, recovered)
			}
			panic(recovered)
		}()
		next.ServeHTTP(w, r)
	})
}

func reportPanic(recovered any) { reportPanicOn(sentry.CurrentHub(), recovered) }

func reportPanicOn(hub *sentry.Hub, recovered any) {
	if !crashReportingActive() {
		return
	}
	hub.Recover(recovered)
	hub.Flush(crashFlushTimeout)
}

func crashReportingActive() bool {
	crashMu.Lock()
	defer crashMu.Unlock()
	return crashActive
}

func crashWithinBudget(now time.Time) bool {
	crashMu.Lock()
	defer crashMu.Unlock()
	cutoff := now.Add(-time.Minute)
	kept := crashWindow[:0]
	for _, at := range crashWindow {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	crashWindow = kept
	if len(crashWindow) >= CrashEventsPerMinute {
		return false
	}
	crashWindow = append(crashWindow, now)
	return true
}
