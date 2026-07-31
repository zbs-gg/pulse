// Package embed implements the bounded JSON-line protocol used by Pulse's
// local embedding helper. Personal product mode uses the strict managed
// protocol; the legacy constructor remains for local developer runtimes.
package embed

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const (
	localDefaultModelPath  = "bge-m3-mlx-fp16"
	localDefaultModelName  = "bge-m3"
	localManagedDimensions = 1024
	// MaxBatchSize is the largest request accepted by the managed helper.
	// Callers that replay an arbitrary backlog must chunk to this boundary.
	MaxBatchSize                = 96
	localMaximumTextBytes       = 32 * 1024
	localMaximumRequestBytes    = 4 * 1024 * 1024
	localMaximumResponseBytes   = 8 * 1024 * 1024
	localMaximumStartupBytes    = 4096
	localStartupTimeout         = 60 * time.Second
	localCallTimeout            = 120 * time.Second
	localManagedProtocolVersion = 1
)

// ManagedVectorContract binds embeddings to the exact model semantics that
// produced the stored vectors. Changing any field requires an explicit
// migration/reindex contract rather than silently mixing vector spaces.
type ManagedVectorContract struct {
	Model        string `json:"model"`
	Source       string `json:"source"`
	Revision     string `json:"revision"`
	Dimensions   int    `json:"dimensions"`
	Pooling      string `json:"pooling"`
	Normalized   bool   `json:"normalized"`
	Opset        int    `json:"opset"`
	Quantization string `json:"quantization"`
}

// ManagedLocalConfig is the engine-neutral v2 process contract for a
// Pulse-owned local runner. Product startup verifies every path, tree digest,
// argument, and vector field before constructing the client.
type ManagedLocalConfig struct {
	Schema                          string
	Protocol                        int
	Engine                          string
	RunnerPath                      string
	RunnerArgs                      []string
	ModelRoot                       string
	SupportRoot                     string
	VectorContract                  ManagedVectorContract
	EmbedderRuntimeActivationDigest string
	EmbedderRuntimeTreeDigest       string
	ModelActivationDigest           string
	ModelTreeDigest                 string
}

type localClientConfig struct {
	callTimeout   time.Duration
	command       string
	commandArgs   []string
	dimensions    int
	environment   []string
	managed       bool
	modelName     string
	responseBytes int
}

// LocalClient embeds through a long-running helper. Exchanges are serialized
// so a response can never be associated with the wrong request.
type LocalClient struct {
	callTimeout   time.Duration
	command       string
	commandArgs   []string
	dimensions    int
	environment   []string
	managed       bool
	modelName     string
	responseBytes int

	mu      sync.Mutex
	cmd     *exec.Cmd
	done    chan error
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	ready   atomic.Bool
	idCount atomic.Uint64
}

func newLocalClient(config localClientConfig) *LocalClient {
	if config.modelName == "" {
		config.modelName = localDefaultModelName
	}
	if config.responseBytes == 0 {
		config.responseBytes = localMaximumResponseBytes
	}
	if config.callTimeout == 0 {
		config.callTimeout = localCallTimeout
	}
	return &LocalClient{
		callTimeout:   config.callTimeout,
		command:       config.command,
		commandArgs:   append([]string(nil), config.commandArgs...),
		dimensions:    config.dimensions,
		environment:   append([]string(nil), config.environment...),
		managed:       config.managed,
		modelName:     config.modelName,
		responseBytes: config.responseBytes,
	}
}

// NewLocal preserves the legacy helper invocation used by development.
// bge-m3 output is still held to the 1024d vector contract.
func NewLocal(pythonExe, helperPath, modelPath, modelName string) *LocalClient {
	if modelPath == "" {
		modelPath = localDefaultModelPath
	}
	if modelName == "" {
		modelName = localDefaultModelName
	}
	dimensions := 0
	if strings.Contains(strings.ToLower(modelName), "bge-m3") {
		dimensions = localManagedDimensions
	}
	return newLocalClient(localClientConfig{
		command:       pythonExe,
		commandArgs:   []string{helperPath, "--model-path", modelPath},
		dimensions:    dimensions,
		modelName:     modelName,
		responseBytes: localMaximumResponseBytes,
	})
}

// NewManagedLocal constructs the strict product-local helper. It does not
// consult PATH, Python installations, API keys, or environment model paths.
func NewManagedLocal(config ManagedLocalConfig) *LocalClient {
	return newLocalClient(localClientConfig{
		command:       config.RunnerPath,
		commandArgs:   config.RunnerArgs,
		dimensions:    config.VectorContract.Dimensions,
		managed:       true,
		modelName:     config.VectorContract.Model,
		responseBytes: localMaximumResponseBytes,
	})
}

// Start spawns the helper and waits for its bounded readiness record.
func (c *LocalClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		if c.ready.Load() {
			return nil
		}
		c.stopLocked()
	}
	if c.command == "" || len(c.commandArgs) == 0 {
		return errors.New("local embed: helper command is incomplete")
	}

	cmd := exec.Command(c.command, c.commandArgs...)
	if c.managed {
		cmd.Env = []string{
			"HOME=" + os.Getenv("HOME"),
			"LANG=" + os.Getenv("LANG"),
			"LC_ALL=" + os.Getenv("LC_ALL"),
			"PULSE_PRODUCT_AUTHORITY_NODE=" + os.Getenv("PULSE_PRODUCT_AUTHORITY_NODE"),
			"PYTHONDONTWRITEBYTECODE=1",
			"PYTHONHASHSEED=0",
			"PYTHONNOUSERSITE=1",
			"TMPDIR=" + os.Getenv("TMPDIR"),
			"TOKENIZERS_PARALLELISM=false",
		}
		cmd.Env = append(cmd.Env, c.environment...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("local embed: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("local embed: stdout pipe: %w", err)
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("local embed: start helper: %w", err)
	}
	c.cmd = cmd
	c.done = make(chan error, 1)
	c.stdin = stdin
	c.stdout = bufio.NewReaderSize(stdout, localMaximumStartupBytes+1)
	go func(done chan error) {
		err := cmd.Wait()
		c.ready.Store(false)
		done <- err
		close(done)
	}(c.done)

	startCtx, cancel := context.WithTimeout(ctx, localStartupTimeout)
	defer cancel()
	line, err := c.readWithContext(startCtx, localMaximumStartupBytes)
	if err != nil {
		c.stopLocked()
		if errors.Is(startCtx.Err(), context.DeadlineExceeded) {
			return errors.New("local embed: helper did not signal ready within timeout")
		}
		return fmt.Errorf("local embed: helper startup read: %w", err)
	}
	if err := c.validateStartup(line); err != nil {
		c.stopLocked()
		return err
	}
	c.ready.Store(true)
	select {
	case <-c.done:
		c.ready.Store(false)
		c.stopLocked()
		return errors.New("local embed: helper exited after startup")
	default:
	}
	return nil
}

func (c *LocalClient) validateStartup(line []byte) error {
	var raw map[string]json.RawMessage
	if err := decodeExactJSON(line, &raw); err != nil {
		return fmt.Errorf("local embed: helper startup parse: %w", err)
	}
	if c.managed {
		expected := []string{"dimensions", "id", "model", "normalized", "ok", "pooling", "protocol", "schema"}
		if !hasExactKeys(raw, expected) {
			return errors.New("local embed: managed helper startup contract has unexpected fields")
		}
		var ack struct {
			Dimensions int    `json:"dimensions"`
			ID         string `json:"id"`
			Model      string `json:"model"`
			Normalized bool   `json:"normalized"`
			OK         bool   `json:"ok"`
			Pooling    string `json:"pooling"`
			Protocol   int    `json:"protocol"`
			Schema     string `json:"schema"`
		}
		if err := decodeExactJSON(line, &ack); err != nil || ack.ID != "__startup__" || !ack.OK ||
			ack.Schema != "pulse.embedder.ready.v1" || ack.Protocol != localManagedProtocolVersion ||
			ack.Model != localDefaultModelName || ack.Dimensions != localManagedDimensions ||
			ack.Pooling != "cls" || !ack.Normalized {
			return errors.New("local embed: managed helper startup contract mismatch")
		}
		return nil
	}
	var ack struct {
		ID    string `json:"id"`
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(line, &ack); err != nil {
		return fmt.Errorf("local embed: helper startup parse: %w", err)
	}
	if ack.Error != "" {
		return fmt.Errorf("local embed: helper startup error: %s", ack.Error)
	}
	if ack.ID != "__startup__" || !ack.OK {
		return errors.New("local embed: unexpected startup ack")
	}
	return nil
}

// Close kills and reaps the helper. It is safe after failed startup.
func (c *LocalClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopLocked()
	return nil
}

func (c *LocalClient) stopLocked() {
	c.ready.Store(false)
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil {
		if c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		if c.done != nil {
			select {
			case <-c.done:
			case <-time.After(5 * time.Second):
			}
		}
	}
	c.cmd = nil
	c.done = nil
	c.stdin = nil
	c.stdout = nil
}

// Model returns the stable model identifier stored with embeddings.
func (c *LocalClient) Model() string { return c.modelName }

// Ready reports live helper process health. It becomes false as soon as the
// managed child exits or any fatal protocol exchange tears it down.
func (c *LocalClient) Ready() bool { return c.ready.Load() }

func (c *LocalClient) writeWithContext(ctx context.Context, payload []byte) error {
	result := make(chan error, 1)
	go func() {
		_, err := c.stdin.Write(payload)
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Embed validates both sides of the helper protocol before returning vectors.
func (c *LocalClient) Embed(ctx context.Context, texts []string, _ InputType) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) > MaxBatchSize {
		return nil, fmt.Errorf("local embed: batch size %d exceeds max %d", len(texts), MaxBatchSize)
	}
	for index, text := range texts {
		if len(text) == 0 || len(text) > localMaximumTextBytes {
			return nil, fmt.Errorf("local embed: text %d must be 1..%d UTF-8 bytes", index, localMaximumTextBytes)
		}
		if !utf8.ValidString(text) {
			return nil, fmt.Errorf("local embed: text %d is invalid", index)
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return nil, errors.New("local embed: helper not started — call Start() first")
	}

	id := fmt.Sprintf("r%d", c.idCount.Add(1))
	request := map[string]any{"id": id, "texts": texts}
	if c.managed {
		request["schema"] = "pulse.embedder.request.v1"
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("local embed: marshal request: %w", err)
	}
	if len(encoded)+1 > localMaximumRequestBytes {
		return nil, errors.New("local embed: request exceeds protocol limit")
	}
	callCtx, cancel := context.WithTimeout(ctx, c.callTimeout)
	defer cancel()
	if !c.ready.Load() {
		c.stopLocked()
		return nil, errors.New("local embed: helper is not running")
	}
	if err := c.writeWithContext(callCtx, append(encoded, '\n')); err != nil {
		c.stopLocked()
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("local embed: helper did not accept request within timeout")
		}
		return nil, fmt.Errorf("local embed: write: %w", err)
	}

	line, err := c.readWithContext(callCtx, c.responseBytes)
	if err != nil {
		c.stopLocked()
		if errors.Is(callCtx.Err(), context.DeadlineExceeded) {
			return nil, errors.New("local embed: helper did not respond within timeout")
		}
		return nil, fmt.Errorf("local embed: read response: %w", err)
	}
	vectors, err := c.decodeResponse(line, id, len(texts))
	if err != nil {
		c.stopLocked()
		return nil, err
	}
	return vectors, nil
}

func (c *LocalClient) decodeResponse(line []byte, id string, textCount int) ([][]float32, error) {
	var response struct {
		Embeddings [][]float32 `json:"embeddings"`
		Error      string      `json:"error"`
		ID         string      `json:"id"`
		Schema     string      `json:"schema"`
	}
	var decodeError error
	if c.managed {
		var managedResponse struct {
			Embeddings [][]float32 `json:"embeddings"`
			ID         string      `json:"id"`
			Schema     string      `json:"schema"`
		}
		decodeError = decodeExactJSON(line, &managedResponse)
		response.Embeddings = managedResponse.Embeddings
		response.ID = managedResponse.ID
		response.Schema = managedResponse.Schema
	} else {
		decodeError = json.Unmarshal(line, &response)
	}
	if decodeError != nil {
		return nil, fmt.Errorf("local embed: parse response: %w", decodeError)
	}
	if response.Error != "" {
		return nil, fmt.Errorf("local embed helper: %s", response.Error)
	}
	if response.ID != id {
		return nil, fmt.Errorf("local embed: id mismatch (sent %s, got %s)", id, response.ID)
	}
	if c.managed && response.Schema != "pulse.embedder.response.v1" {
		return nil, errors.New("local embed: managed helper response schema mismatch")
	}
	if len(response.Embeddings) != textCount {
		return nil, fmt.Errorf("local embed: got %d vectors for %d texts", len(response.Embeddings), textCount)
	}
	for vectorIndex, vector := range response.Embeddings {
		if c.dimensions > 0 && len(vector) != c.dimensions {
			return nil, fmt.Errorf("local embed: vector %d has dimension %d, expected %d", vectorIndex, len(vector), c.dimensions)
		}
		var normSquared float64
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return nil, fmt.Errorf("local embed: vector %d contains a non-finite value", vectorIndex)
			}
			normSquared += float64(value) * float64(value)
		}
		norm := math.Sqrt(normSquared)
		if math.Abs(norm-1) > 0.005 {
			return nil, fmt.Errorf("local embed: vector %d is not unit normalized (norm %.6f)", vectorIndex, norm)
		}
	}
	return response.Embeddings, nil
}

func (c *LocalClient) readWithContext(ctx context.Context, maximum int) ([]byte, error) {
	type result struct {
		line []byte
		err  error
	}
	channel := make(chan result, 1)
	go func() {
		line := make([]byte, 0, min(maximum, 64*1024))
		for {
			fragment, err := c.stdout.ReadSlice('\n')
			if len(line)+len(fragment) > maximum {
				channel <- result{err: errors.New("protocol line exceeds limit")}
				return
			}
			line = append(line, fragment...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				channel <- result{err: err}
				return
			}
			channel <- result{line: bytes.TrimSuffix(line, []byte{'\n'})}
			return
		}
	}()
	select {
	case value := <-channel:
		return value.line, value.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func decodeExactJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("multiple JSON values")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func hasExactKeys(raw map[string]json.RawMessage, expected []string) bool {
	if len(raw) != len(expected) {
		return false
	}
	for _, key := range expected {
		if _, ok := raw[key]; !ok {
			return false
		}
	}
	return true
}
