package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/store"
)

func TestListenTeamDaemonBindsAndVerifiesConcreteLoopbackAddress(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatalf("listenTeamDaemon: %v", err)
	}
	defer listener.Close()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !tcpAddr.IP.IsLoopback() || tcpAddr.Port == 0 {
		t.Fatalf("listener address = %T %v, want concrete loopback TCP address", listener.Addr(), listener.Addr())
	}
}

func TestListenTeamDaemonReturnsBindFailureSynchronously(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	listener, err := listenTeamDaemon(occupied.Addr().String())
	if err == nil {
		listener.Close()
		t.Fatal("listenTeamDaemon accepted an address that was already bound")
	}
}

type rotatingLeaseRenewer struct {
	lease store.TeamWriterLease
}

func (renewer rotatingLeaseRenewer) AcquireTeamWriterLease(context.Context, store.TeamWriterLeaseRequest) (store.TeamWriterLease, error) {
	return renewer.lease, nil
}

func TestRenewTeamWriterLeaseTreatsTokenRotationAsTerminal(t *testing.T) {
	original := store.TeamWriterLease{
		StoreID: "store_1", TeamID: "team_1", WriterID: "writer_1",
		WriterVersion: 34, Token: "original-token",
	}
	rotated := original
	rotated.Token = "rotated-token"

	err := renewTeamWriterLeaseAtInterval(context.Background(), rotatingLeaseRenewer{lease: rotated}, original, time.Millisecond)
	if !errors.Is(err, errTeamWriterLeaseChanged) {
		t.Fatalf("renewal error = %v, want %v", err, errTeamWriterLeaseChanged)
	}
}

func TestServeTeamRuntimeLeaseFailureCancelsRequestsAndHardCloses(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	handler := http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	})
	server := &http.Server{Handler: handler}
	leaseLost := errors.New("lease lost")
	resultCh := make(chan teamRuntimeResult, 1)
	go func() {
		resultCh <- serveTeamRuntime(server, listener, teamRuntimeOptions{
			Signals: make(chan os.Signal),
			RenewLease: func(context.Context) error {
				<-started
				return leaseLost
			},
			ShutdownTimeout: time.Second,
			QuiesceTimeout:  time.Second,
		})
	}()

	clientDone := make(chan error, 1)
	go func() {
		response, requestErr := http.Get("http://" + listener.Addr().String() + "/blocked")
		if response != nil {
			response.Body.Close()
		}
		clientDone <- requestErr
	}()

	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("active request context was not canceled after writer lease failure")
	}
	var result teamRuntimeResult
	select {
	case result = <-resultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("team runtime did not stop after writer lease failure")
	}
	if !errors.Is(result.Err, leaseLost) {
		t.Fatalf("runtime error = %v, want lease failure", result.Err)
	}
	if result.ReleaseLease {
		t.Fatal("runtime allowed lease release after ownership became unprovable")
	}
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client connection remained active after hard close")
	}
}

func TestServeTeamRuntimeLeaseFailureDuringGracefulDrainAbortsImmediately(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	failRenewal := make(chan struct{})
	signals := make(chan os.Signal, 1)
	leaseLost := errors.New("lease lost during drain")
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	})}
	resultCh := make(chan teamRuntimeResult, 1)
	go func() {
		resultCh <- serveTeamRuntime(server, listener, teamRuntimeOptions{
			Signals: signals,
			RenewLease: func(context.Context) error {
				<-failRenewal
				return leaseLost
			},
			ShutdownTimeout: 2 * time.Second,
			QuiesceTimeout:  time.Second,
		})
	}()
	clientDone := make(chan struct{})
	go func() {
		response, _ := http.Get("http://" + listener.Addr().String() + "/blocked")
		if response != nil {
			response.Body.Close()
		}
		close(clientDone)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	signals <- os.Interrupt
	close(failRenewal)
	select {
	case <-canceled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("lease loss during graceful drain did not hard-cancel active request")
	}
	result := <-resultCh
	if !errors.Is(result.Err, leaseLost) {
		t.Fatalf("runtime error = %v, want lease failure", result.Err)
	}
	if result.ReleaseLease {
		t.Fatal("runtime allowed release after losing lease during graceful drain")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("client remained blocked after lease-loss hard close")
	}
}

func TestServeTeamRuntimeShutdownTimeoutLeavesLeaseToExpireWhenHandlerWillNotQuiesce(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	releaseHandler := make(chan struct{})
	handlerDone := make(chan struct{})
	signals := make(chan os.Signal, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-releaseHandler
		close(handlerDone)
	})}
	resultCh := make(chan teamRuntimeResult, 1)
	go func() {
		resultCh <- serveTeamRuntime(server, listener, teamRuntimeOptions{
			Signals: signals,
			RenewLease: func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			},
			ShutdownTimeout: 60 * time.Millisecond,
			QuiesceTimeout:  80 * time.Millisecond,
		})
	}()
	clientDone := make(chan struct{})
	go func() {
		response, _ := http.Get("http://" + listener.Addr().String() + "/stuck")
		if response != nil {
			response.Body.Close()
		}
		close(clientDone)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	signals <- os.Interrupt
	var result teamRuntimeResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("runtime did not return after forced-close quiescence budget")
	}
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("runtime error = %v, want graceful shutdown deadline", result.Err)
	}
	if result.ReleaseLease {
		t.Fatal("runtime allowed lease release while a handler remained active")
	}
	select {
	case <-handlerDone:
		t.Fatal("test handler unexpectedly quiesced before its barrier was released")
	default:
	}
	close(releaseHandler)
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("blocked handler did not exit after test barrier release")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("client did not exit after forced close")
	}
}

func TestServeTeamRuntimeKeepsLeaseRenewalAliveDuringGracefulDrain(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	releaseHandler := make(chan struct{})
	renewalCanceled := make(chan struct{})
	signals := make(chan os.Signal, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-releaseHandler
	})}
	resultCh := make(chan teamRuntimeResult, 1)
	go func() {
		resultCh <- serveTeamRuntime(server, listener, teamRuntimeOptions{
			Signals: signals,
			RenewLease: func(ctx context.Context) error {
				<-ctx.Done()
				close(renewalCanceled)
				return nil
			},
			ShutdownTimeout: time.Second,
			QuiesceTimeout:  time.Second,
		})
	}()
	clientDone := make(chan struct{})
	go func() {
		response, _ := http.Get("http://" + listener.Addr().String() + "/drain")
		if response != nil {
			response.Body.Close()
		}
		close(clientDone)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	signals <- os.Interrupt
	select {
	case <-renewalCanceled:
		t.Fatal("lease renewal was canceled before the active request drained")
	case <-time.After(75 * time.Millisecond):
	}
	close(releaseHandler)
	result := <-resultCh
	if result.Err != nil {
		t.Fatalf("graceful runtime error: %v", result.Err)
	}
	if !result.ReleaseLease {
		t.Fatal("quiesced graceful shutdown did not permit lease release")
	}
	select {
	case <-renewalCanceled:
	case <-time.After(time.Second):
		t.Fatal("lease renewal was not stopped after graceful drain")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("client did not complete after graceful drain")
	}
}

func TestServeTeamRuntimeDoesNotReleaseWhenRenewalReportsFailureWhileStopping(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	leaseLost := errors.New("lease ownership changed while stopping")
	resultCh := make(chan teamRuntimeResult, 1)
	go func() {
		resultCh <- serveTeamRuntime(&http.Server{Handler: http.NotFoundHandler()}, listener, teamRuntimeOptions{
			Signals: signals,
			RenewLease: func(ctx context.Context) error {
				<-ctx.Done()
				return leaseLost
			},
			ShutdownTimeout: time.Second,
			QuiesceTimeout:  time.Second,
		})
	}()
	signals <- os.Interrupt
	result := <-resultCh
	if !errors.Is(result.Err, leaseLost) {
		t.Fatalf("runtime error = %v, want late renewal failure", result.Err)
	}
	if result.ReleaseLease {
		t.Fatal("runtime allowed lease release after renewal reported failure while stopping")
	}
}
