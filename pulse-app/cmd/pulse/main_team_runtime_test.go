package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nkkmnk/pulse/internal/embed"
	"github.com/nkkmnk/pulse/internal/store"
)

func TestComposeTeamRuntimeHandlerKeepsOwnerAdministrationOnItsOwnRouter(t *testing.T) {
	team := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handler", "team")
		w.WriteHeader(http.StatusNoContent)
	})
	owner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handler", "owner")
		w.WriteHeader(http.StatusNoContent)
	})
	airlock := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Handler", "airlock")
		w.WriteHeader(http.StatusNoContent)
	})
	handler := composeTeamRuntimeHandler(team, owner, airlock)

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "/team/v1/status", want: "team"},
		{path: "/team/v1/owner/approval", want: "owner"},
		{path: "/team/v1/owner/not-registered", want: "owner"},
		{path: "/airlock/team-publication", want: "airlock"},
		{path: "/airlock/team-publication/near", want: "team"},
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, test.path, nil))
		if got := recorder.Header().Get("X-Handler"); got != test.want {
			t.Fatalf("%s routed to %q, want %q", test.path, got, test.want)
		}
	}
}

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

func TestServeTeamRuntimeDeletionWorkerFailureCancelsRequestsAndIsFatal(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	canceled := make(chan struct{})
	workerLostLease := errors.New("deletion worker lost writer lease")
	server := &http.Server{Handler: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		close(canceled)
	})}
	resultCh := make(chan teamRuntimeResult, 1)
	go func() {
		resultCh <- serveTeamRuntime(server, listener, teamRuntimeOptions{
			Signals: make(chan os.Signal),
			RenewLease: func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			},
			RunBackgroundWorkers: func(context.Context) error {
				<-started
				return workerLostLease
			},
			ShutdownTimeout: time.Second,
			QuiesceTimeout:  time.Second,
		})
	}()
	go func() {
		response, _ := http.Get("http://" + listener.Addr().String() + "/blocked")
		if response != nil {
			response.Body.Close()
		}
	}()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("deletion worker failure did not cancel active request")
	}
	result := <-resultCh
	if !errors.Is(result.Err, workerLostLease) {
		t.Fatalf("runtime error = %v, want worker failure", result.Err)
	}
	if result.ReleaseLease {
		t.Fatal("runtime allowed lease release after deletion worker lost ownership")
	}
}

func TestServeTeamRuntimeStopsDeletionWorkerBeforeReleasingLease(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	workerStopped := make(chan struct{})
	resultCh := make(chan teamRuntimeResult, 1)
	go func() {
		resultCh <- serveTeamRuntime(
			&http.Server{Handler: http.NotFoundHandler()}, listener,
			teamRuntimeOptions{
				Signals: signals,
				RenewLease: func(ctx context.Context) error {
					<-ctx.Done()
					return nil
				},
				RunBackgroundWorkers: func(ctx context.Context) error {
					<-ctx.Done()
					close(workerStopped)
					return nil
				},
				ShutdownTimeout: time.Second,
				QuiesceTimeout:  time.Second,
			},
		)
	}()
	signals <- os.Interrupt
	result := <-resultCh
	if result.Err != nil || !result.ReleaseLease {
		t.Fatalf("graceful runtime result = %+v", result)
	}
	select {
	case <-workerStopped:
	default:
		t.Fatal("runtime released lease before deletion worker stopped")
	}
}

func TestServeTeamRuntimeBoundsDeletionWorkerShutdownAndLeavesLeaseToExpire(t *testing.T) {
	listener, err := listenTeamDaemon("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	signals := make(chan os.Signal, 1)
	workerStarted := make(chan struct{})
	releaseWorker := make(chan struct{})
	resultCh := make(chan teamRuntimeResult, 1)
	go func() {
		resultCh <- serveTeamRuntime(
			&http.Server{Handler: http.NotFoundHandler()}, listener,
			teamRuntimeOptions{
				Signals: signals,
				RenewLease: func(ctx context.Context) error {
					<-ctx.Done()
					return nil
				},
				RunBackgroundWorkers: func(context.Context) error {
					close(workerStarted)
					<-releaseWorker
					return nil
				},
				ShutdownTimeout: 50 * time.Millisecond,
				QuiesceTimeout:  50 * time.Millisecond,
			},
		)
	}()
	<-workerStarted
	signals <- os.Interrupt
	select {
	case result := <-resultCh:
		if !errors.Is(result.Err, errTeamBackgroundWorkerStopTimeout) || result.ReleaseLease {
			t.Fatalf("stuck worker result = %+v", result)
		}
		close(releaseWorker)
	case <-time.After(250 * time.Millisecond):
		close(releaseWorker)
		<-resultCh
		t.Fatal("runtime waited without bound for deletion worker shutdown")
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

func TestRunTeamBackgroundWorkersCancelsPeerAfterProjectionFailure(t *testing.T) {
	projectionFailure := errors.New("projection writer lease lost")
	peerCanceled := make(chan struct{})
	err := runTeamBackgroundWorkers(context.Background(), time.Second,
		teamRuntimeWorker{Name: "deletion", Run: func(ctx context.Context) error {
			<-ctx.Done()
			close(peerCanceled)
			return nil
		}},
		teamRuntimeWorker{Name: "projection", Run: func(context.Context) error {
			return projectionFailure
		}},
	)
	if !errors.Is(err, projectionFailure) {
		t.Fatalf("background worker error = %v, want projection failure", err)
	}
	select {
	case <-peerCanceled:
	case <-time.After(time.Second):
		t.Fatal("projection failure did not cancel deletion peer")
	}
}

func TestRunTeamBackgroundWorkersBoundsCanceledPeerJoinAfterFailure(t *testing.T) {
	projectionFailure := errors.New("projection writer lease lost")
	peerCanceled := make(chan struct{})
	releasePeer := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- runTeamBackgroundWorkers(context.Background(), 20*time.Millisecond,
			teamRuntimeWorker{Name: "deletion", Run: func(ctx context.Context) error {
				<-ctx.Done()
				close(peerCanceled)
				<-releasePeer
				return nil
			}},
			teamRuntimeWorker{Name: "projection", Run: func(context.Context) error {
				return projectionFailure
			}},
		)
	}()
	select {
	case <-peerCanceled:
	case <-time.After(time.Second):
		t.Fatal("projection failure did not cancel deletion peer")
	}
	select {
	case err := <-result:
		if !errors.Is(err, projectionFailure) {
			t.Fatalf("background worker error = %v, want projection failure", err)
		}
		if !errors.Is(err, errTeamBackgroundWorkerStopTimeout) {
			t.Fatalf("background worker error = %v, want bounded peer-stop timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("background workers did not return after bounded peer join")
	}
	close(releasePeer)
}

func TestRunTeamBackgroundWorkersWaitsForEveryWorkerOnGracefulStop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan string, 2)
	result := make(chan error, 1)
	worker := func(name string) teamRuntimeWorker {
		return teamRuntimeWorker{Name: name, Run: func(ctx context.Context) error {
			<-ctx.Done()
			stopped <- name
			return nil
		}}
	}
	go func() {
		result <- runTeamBackgroundWorkers(ctx, time.Second, worker("deletion"), worker("projection"))
	}()
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("graceful worker stop = %v", err)
	}
	got := map[string]bool{<-stopped: true, <-stopped: true}
	if !got["deletion"] || !got["projection"] {
		t.Fatalf("stopped workers = %v", got)
	}
}

type teamRuntimeEmbedderFake struct{}

func (teamRuntimeEmbedderFake) Model() string { return "bge-m3:test" }

func (teamRuntimeEmbedderFake) Embed(
	context.Context,
	[]string,
	embed.InputType,
) ([][]float32, error) {
	return [][]float32{{1, 0}}, nil
}

type teamRuntimeEmbeddingStoreFake struct{}

func (*teamRuntimeEmbeddingStoreFake) ReadTeamEmbeddingProjectionInput(
	context.Context,
	store.TeamEmbeddingProjectionInputRequest,
) (store.TeamEmbeddingProjectionInput, error) {
	return store.TeamEmbeddingProjectionInput{}, nil
}

func (*teamRuntimeEmbeddingStoreFake) CompleteTeamMemoryEmbeddingProjection(
	context.Context,
	store.TeamMemoryEmbeddingProjectionRequest,
) (store.TeamProjectionCompletionResult, error) {
	return store.TeamProjectionCompletionResult{}, nil
}

func (*teamRuntimeEmbeddingStoreFake) CompleteTeamSemanticEmbeddingProjection(
	context.Context,
	store.TeamSemanticEmbeddingProjectionRequest,
) (store.TeamProjectionCompletionResult, error) {
	return store.TeamProjectionCompletionResult{}, nil
}

func TestTeamEmbeddingProjectionProcessorWiringIsExplicitlyOptional(t *testing.T) {
	missing, err := newTeamEmbeddingProjectionProcessor(nil, nil)
	if err != nil || missing != nil {
		t.Fatalf("missing dependency = %#v, %v", missing, err)
	}
	configured, err := newTeamEmbeddingProjectionProcessor(
		&teamRuntimeEmbeddingStoreFake{}, teamRuntimeEmbedderFake{},
	)
	if err != nil {
		t.Fatal(err)
	}
	health := configured.ProjectionDependencyHealth()
	if health.State != store.TeamProjectionDependencyReady || health.Reason != "" {
		t.Fatalf("configured health = %+v", health)
	}
}

func TestTeamEmbedderNeverDiscoversPersonalCredentialPaths(t *testing.T) {
	t.Setenv("COHERE_API_KEY", "personal-key-must-be-ignored")
	t.Setenv("PULSE_TEAM_COHERE_KEY_FILE", "")
	t.Setenv("PULSE_TEAM_LOCAL_EMBED_PYTHON", "")
	t.Setenv("PULSE_TEAM_LOCAL_EMBED_HELPER", "")
	t.Setenv("PULSE_TEAM_LOCAL_EMBED_MODEL", "")
	embedder, name, err := teamEmbedderFromEnv()
	if err != nil || embedder != nil || name != "" {
		t.Fatalf("team embedder discovered personal config: embedder=%T name=%q err=%v", embedder, name, err)
	}
}

func TestTeamEmbedderRequiresSeparatePrivateCredentialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "team-cohere-key")
	if err := os.WriteFile(path, []byte("team-only-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PULSE_TEAM_COHERE_KEY_FILE", path)
	if _, _, err := teamEmbedderFromEnv(); err == nil {
		t.Fatal("team embedder accepted group/world-readable credential")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	embedder, name, err := teamEmbedderFromEnv()
	if err != nil || embedder == nil || name != "embed-v4.0" {
		t.Fatalf("team credential embedder=%T name=%q err=%v", embedder, name, err)
	}
}

func TestTeamEmbedderRejectsPartialLocalDependency(t *testing.T) {
	t.Setenv("PULSE_TEAM_COHERE_KEY_FILE", "")
	t.Setenv("PULSE_TEAM_LOCAL_EMBED_PYTHON", "/usr/bin/python3")
	t.Setenv("PULSE_TEAM_LOCAL_EMBED_HELPER", "")
	t.Setenv("PULSE_TEAM_LOCAL_EMBED_MODEL", "")
	if _, _, err := teamEmbedderFromEnv(); err == nil {
		t.Fatal("team embedder accepted a partial local dependency")
	}
}

func TestSyntheticPublicationAirlockIsDefaultOffAndRejectsPartialConfiguration(t *testing.T) {
	for _, name := range []string{
		"PULSE_TEAM_PUBLICATION_DEPLOYMENT_ID",
		"PULSE_TEAM_SHARED_PROJECT_ID",
		"PULSE_TEAM_AIRLOCK_ORIGIN",
		"PULSE_TEAM_AIRLOCK_SYNTHETIC_CANDIDATE_FILE",
		"PULSE_TEAM_PUBLICATION_SYNTHETIC_ONLY",
	} {
		t.Setenv(name, "")
	}
	handler, err := newSyntheticTeamPublicationAirlock(nil, nil, store.TeamWriterLease{}, nil)
	if err != nil || handler != nil {
		t.Fatalf("default-off Airlock handler=%v err=%v", handler, err)
	}
	t.Setenv("PULSE_TEAM_PUBLICATION_DEPLOYMENT_ID", "deployment_partial")
	if _, err := newSyntheticTeamPublicationAirlock(nil, nil, store.TeamWriterLease{}, nil); err == nil {
		t.Fatal("partial Airlock configuration was accepted")
	}
}
