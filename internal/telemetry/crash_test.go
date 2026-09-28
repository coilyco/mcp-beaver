package telemetry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

type captureTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (c *captureTransport) Configure(sentry.ClientOptions)        {}
func (c *captureTransport) Flush(time.Duration) bool              { return true }
func (c *captureTransport) FlushWithContext(context.Context) bool { return true }
func (c *captureTransport) Close()                                {}
func (c *captureTransport) SendEvent(event *sentry.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event)
}

func (c *captureTransport) sent() []*sentry.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*sentry.Event(nil), c.events...)
}

func withCapturedCrashes(t *testing.T) *captureTransport {
	t.Helper()
	transport := &captureTransport{}
	active, err := startCrashReporting("https://public@example.invalid/1", "jev-mcp", transport)
	if err != nil || !active {
		t.Fatalf("startCrashReporting = %v, %v", active, err)
	}
	t.Cleanup(func() {
		_, _ = startCrashReporting("", "", nil)
		crashMu.Lock()
		crashWindow = nil
		crashMu.Unlock()
	})
	return transport
}

func TestNoDSNLeavesCrashReportingOff(t *testing.T) {
	active, err := startCrashReporting("", "jev-mcp", nil)
	if active || err != nil {
		t.Fatalf("startCrashReporting(\"\") = %v, %v, want off and no error", active, err)
	}
	ReportCrash(errors.New("ignored"))
}

func TestReportCrashSendsTheErrorTaggedWithTheServer(t *testing.T) {
	transport := withCapturedCrashes(t)
	ReportCrash(errors.New("listen tcp :8080: address already in use"))
	sent := transport.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d events, want 1", len(sent))
	}
	if got := sent[0].Exception[len(sent[0].Exception)-1].Value; got != "listen tcp :8080: address already in use" {
		t.Fatalf("exception value = %q", got)
	}
	if got := sent[0].Tags["server.name"]; got != "jev-mcp" {
		t.Fatalf("server.name tag = %q", got)
	}
}

func TestRecoverCrashReportsAndStillPanics(t *testing.T) {
	transport := withCapturedCrashes(t)
	defer func() {
		if recovered := recover(); recovered != "spec wedged" {
			t.Fatalf("re-panic = %v, want the original panic", recovered)
		}
		if len(transport.sent()) != 1 {
			t.Fatalf("sent %d events, want 1", len(transport.sent()))
		}
	}()
	func() {
		defer RecoverCrash()
		panic("spec wedged")
	}()
}

func TestRecoverHandlerReportsAPanicAndLetsNetHTTPHandleIt(t *testing.T) {
	transport := withCapturedCrashes(t)
	server := httptest.NewServer(RecoverHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler blew up")
	})))
	defer server.Close()
	server.Config.ErrorLog = nil
	resp, err := http.Get(server.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatalf("request succeeded, want net/http to drop the connection as it does without the wrapper")
	}
	if len(transport.sent()) != 1 {
		t.Fatalf("sent %d events, want 1", len(transport.sent()))
	}
}

func TestRecoverHandlerIgnoresADeliberateAbort(t *testing.T) {
	transport := withCapturedCrashes(t)
	server := httptest.NewServer(RecoverHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})))
	defer server.Close()
	if resp, err := http.Get(server.URL); err == nil {
		resp.Body.Close()
	}
	if len(transport.sent()) != 0 {
		t.Fatalf("sent %d events for http.ErrAbortHandler, want 0", len(transport.sent()))
	}
}

func TestCrashBudgetCapsEventsPerProcessMinute(t *testing.T) {
	withCapturedCrashes(t)
	start := time.Unix(1000, 0)
	allowed := 0
	for range CrashEventsPerMinute + 1 {
		if crashWithinBudget(start) {
			allowed++
		}
	}
	if allowed != CrashEventsPerMinute {
		t.Fatalf("allowed %d in one minute, want %d", allowed, CrashEventsPerMinute)
	}
	if !crashWithinBudget(start.Add(61 * time.Second)) {
		t.Fatalf("budget did not recover after the window")
	}
}
