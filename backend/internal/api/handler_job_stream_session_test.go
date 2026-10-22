package api

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/MeowSalty/LinguaFlow/backend/internal/config"
	"github.com/MeowSalty/LinguaFlow/backend/internal/ent"
	"github.com/MeowSalty/LinguaFlow/backend/internal/event"
	"github.com/MeowSalty/LinguaFlow/backend/internal/service"
	"github.com/golang-jwt/jwt/v5"
)

func serverStreamFixture(t *testing.T, eventCount int) (*Server, *ent.Client, *ent.User, *ent.Job) {
	t.Helper()
	s, client, account := jobStreamTestServer(t, 16, 10, 20)
	s.mode, s.localUser = config.ModeServer, nil
	s.authService = service.NewAuthService(client, service.AuthConfig{Secret: []byte(authTestSecret), Issuer: authTestIssuer}, nil)
	job, err := seedJobEventsN(t, client, account, eventCount)
	if err != nil {
		t.Fatal(err)
	}
	return s, client, account, job
}

func streamBearerRequest(t *testing.T, account *ent.User, expiresAt time.Time) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(t.Context())
	r.Header.Set("Authorization", "Bearer "+authTestToken(t, account, expiresAt, []byte(authTestSecret)))
	return r
}

func TestJobStreamSessionRevocationDuringReplay(t *testing.T) {
	s, client, account, job := serverStreamFixture(t, 3)
	w := &revokingStreamWriter{safeRecorder: newSafeRecorder(), trigger: "id: 1", revoke: func() {
		client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
	}}
	s.handleJobStream(w, streamBearerRequest(t, account, time.Now().Add(time.Hour)), job.ID)
	if ids := parseIDLines(w.String()); len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("stream continued after version revocation: %v", ids)
	}
}

func TestJobStreamSessionRevocationIdleAndLive(t *testing.T) {
	for _, cause := range []string{"version", "disabled", "database"} {
		for _, live := range []bool{false, true} {
			name := cause + "/idle"
			if live {
				name = cause + "/live"
			}
			t.Run(name, func(t *testing.T) {
				s, client, account, job := serverStreamFixture(t, 0)
				s.eventBroker = event.NewBroker(nil)
				w := &revokingStreamWriter{safeRecorder: newSafeRecorder(), trigger: ": connected", revoke: func() {
					switch cause {
					case "version":
						client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
					case "disabled":
						client.User.UpdateOneID(account.ID).SetActive(false).ExecX(t.Context())
					case "database":
						if err := client.Close(); err != nil {
							t.Error(err)
						}
					}
					if live {
						s.eventBroker.Publish(job.ID, event.Event{JobID: job.ID, Seq: 1, Type: "translation", Message: "private"})
					}
				}}
				started := time.Now()
				s.handleJobStreamWithInterval(w, streamBearerRequest(t, account, time.Now().Add(time.Hour)), job.ID, 10*time.Millisecond)
				if time.Since(started) >= 3*time.Second {
					t.Fatal("stream did not close after authentication became invalid")
				}
				if countIDLines(w.String()) != 0 || strings.Contains(w.String(), ": keepalive") {
					t.Fatalf("stream wrote after authentication failed: %s", w.String())
				}
			})
		}
	}
}

func TestJobStreamFreezesOriginalSessionExpiry(t *testing.T) {
	s, _, account, job := serverStreamFixture(t, 0)
	expiresAt := time.Now().Add(150 * time.Millisecond)
	claims := &service.AccessTokenClaims{UserID: account.ID, AuthVersion: account.AuthVersion,
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: &jwt.NumericDate{Time: expiresAt}}}
	r := httptest.NewRequest(http.MethodGet, "/stream", nil).WithContext(context.WithValue(t.Context(), authContextKey{}, authenticatedUser{User: account, Claims: claims}))
	w := &revokingStreamWriter{safeRecorder: newSafeRecorder(), trigger: ": connected", revoke: func() {
		// Even an updated authentication context cannot renew an established stream.
		claims.ExpiresAt = jwt.NewNumericDate(time.Now().Add(time.Hour))
	}}
	started := time.Now()
	s.handleJobStreamWithInterval(w, r, job.ID, 10*time.Millisecond)
	if elapsed := time.Since(started); elapsed < 100*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("original access expiry not respected: %s", elapsed)
	}
}

func TestJobStreamRequiresCompleteServerClaims(t *testing.T) {
	s, _, account, job := serverStreamFixture(t, 0)
	w := httptest.NewRecorder()
	s.handleJobStream(w, withAuthUser(httptest.NewRequest(http.MethodGet, "/stream", nil), account), job.ID)
	accountAssertProblem(t, w, http.StatusUnauthorized, "urn:linguaflow:token-invalid")
}

func TestJobStreamHTTPWriterSupportsRevocation(t *testing.T) {
	s, client, account, job := serverStreamFixture(t, 0)
	done := make(chan struct{})
	router := s.newRouter()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		router.ServeHTTP(w, r)
	}))
	defer server.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/api/v1/jobs/"+strconv.Itoa(job.ID)+"/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+authTestToken(t, account, time.Now().Add(time.Hour), []byte(authTestSecret)))
	clientHTTP := server.Client()
	clientHTTP.Timeout = 12 * time.Second
	response, err := clientHTTP.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != ": connected\n" {
		t.Fatalf("stream handshake: %q %v", line, err)
	}
	client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
	if _, err := io.Copy(io.Discard, reader); err != nil {
		t.Fatalf("stream did not close cleanly: %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("stream handler did not return")
	}
}

type deadlineBlockingStreamWriter struct {
	*safeRecorder
	deadline   time.Time
	blockFlush bool
	blocked    bool
	revoke     func()
}

func (w *deadlineBlockingStreamWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func (w *deadlineBlockingStreamWriter) block() error {
	w.blocked = true
	w.revoke()
	timer := time.NewTimer(time.Until(w.deadline))
	defer timer.Stop()
	<-timer.C
	return os.ErrDeadlineExceeded
}

func (w *deadlineBlockingStreamWriter) Write(data []byte) (int, error) {
	if !w.blockFlush && strings.Contains(string(data), "event:") {
		return 0, w.block()
	}
	return w.safeRecorder.Write(data)
}

func (w *deadlineBlockingStreamWriter) FlushError() error {
	if w.blockFlush {
		return w.block()
	}
	w.safeRecorder.Flush()
	return nil
}

func TestJobStreamBoundsBlockedWriteAndFlush(t *testing.T) {
	for _, flush := range []bool{false, true} {
		name := "write"
		if flush {
			name = "flush"
		}
		t.Run(name, func(t *testing.T) {
			s, client, account, job := serverStreamFixture(t, 1)
			w := &deadlineBlockingStreamWriter{safeRecorder: newSafeRecorder(), blockFlush: flush, revoke: func() {
				client.User.UpdateOneID(account.ID).AddAuthVersion(1).ExecX(t.Context())
			}}
			started := time.Now()
			s.handleJobStream(w, streamBearerRequest(t, account, time.Now().Add(time.Hour)), job.ID)
			if elapsed := time.Since(started); !w.blocked || elapsed > 7*time.Second {
				t.Fatalf("write deadline failed: blocked=%t elapsed=%s", w.blocked, elapsed)
			}
			if countIDLines(w.String()) != 0 || strings.Contains(w.String(), ": keepalive") {
				t.Fatalf("stream continued after blocked write: %s", w.String())
			}
		})
	}
}
