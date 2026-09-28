package telemetry

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHandlerPanicCarriesTheRequestWithoutCredentials(t *testing.T) {
	transport := withCapturedCrashes(t)
	server := httptest.NewServer(RecoverHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("handler blew up")
	})))
	defer server.Close()
	server.Config.ErrorLog = nil
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/mcp", strings.NewReader(`{"arguments":"MEMBER-TEXT"}`))
	req.Header.Set("Authorization", "Bearer "+strings.Join([]string{"secret", "token"}, "-"))
	req.Header.Set("X-Agent-Origin", "eng-platform/beetle-ox:fixture")
	if resp, err := http.DefaultClient.Do(req); err == nil {
		resp.Body.Close()
	}
	sent := transport.sent()
	if len(sent) != 1 || sent[0].Request == nil {
		t.Fatalf("sent %d events with request %v, want 1 carrying the request", len(sent), sent)
	}
	request := sent[0].Request
	if request.Method != http.MethodPost || !strings.HasSuffix(request.URL, "/mcp") {
		t.Fatalf("request = %s %s, want POST .../mcp", request.Method, request.URL)
	}
	if request.Headers["X-Agent-Origin"] != "eng-platform/beetle-ox:fixture" {
		t.Fatalf("headers lost the caller origin: %v", request.Headers)
	}
	for name, value := range request.Headers {
		if strings.Contains(value, "secret-token") {
			t.Fatalf("the %s credential reached Sentry: %v", name, request.Headers)
		}
	}
	if strings.Contains(request.Data, "MEMBER-TEXT") {
		t.Fatalf("request body reached Sentry: %q", request.Data)
	}
}

func TestACrashCarriesLogBreadcrumbsAndNeverRaisesOne(t *testing.T) {
	transport := withCapturedCrashes(t)
	logger := slog.New(WithCrashBreadcrumbs(slog.NewJSONHandler(io.Discard, nil)))
	logger.Info("tool call served", slog.String("tool", "create_noul_decision"), slog.String("arguments", "MEMBER-TEXT"))
	if len(transport.sent()) != 0 {
		t.Fatalf("a log line raised an event")
	}
	ReportCrash(errors.New("serve: listener closed"))
	crumbs := transport.sent()[0].Breadcrumbs
	if len(crumbs) != 1 || crumbs[0].Message != "tool call served" {
		t.Fatalf("breadcrumbs = %+v, want the one log record", crumbs)
	}
	if crumbs[0].Data["tool"] != "create_noul_decision" || crumbs[0].Data["arguments"] != "[Filtered]" {
		t.Fatalf("breadcrumb data = %v, want tool kept and arguments filtered", crumbs[0].Data)
	}
}
