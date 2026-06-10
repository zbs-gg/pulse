// Local MLX embedder — spawns a long-running Python helper subprocess
// that loads bge-m3 (or any MLX-format sentence-transformer) once and
// embeds batches over line-delimited JSON on stdin/stdout.
//
// Drop-in replacement for *CohereClient anywhere the Embedder interface
// is expected. Returns L2-normalized float32 vectors. dim depends on
// model (bge-m3 = 1024, matching Cohere embed-v4.0 baseline).
//
// Use when no Cohere API key is available and you want fully local /
// free embedding. Cost: ~30ms per text on M4 Max (bge-m3, fp16),
// ~1.1GB RAM for the model.
package embed

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

const (
	localDefaultModelPath = "bge-m3-mlx-fp16"
	localDefaultModelName = "bge-m3"
	localStartupTimeout   = 60 * time.Second
	localCallTimeout      = 120 * time.Second
)

// LocalClient embeds via a Python subprocess. Safe for concurrent use —
// requests are serialized through a mutex so the helper sees one batch
// at a time on stdin.
type LocalClient struct {
	helperPath string // path to mlx_embed_helper.py
	pythonExe  string // python interpreter (e.g. ./.venv/bin/python)
	modelPath  string
	modelName  string

	mu      sync.Mutex // serializes stdin/stdout exchange
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	idCount atomic.Uint64
}

// NewLocal creates an embedder backed by mlx_embed_helper.py. The helper
// is NOT started here — call Start() before first use.
func NewLocal(pythonExe, helperPath, modelPath, modelName string) *LocalClient {
	if modelPath == "" {
		modelPath = localDefaultModelPath
	}
	if modelName == "" {
		modelName = localDefaultModelName
	}
	return &LocalClient{
		helperPath: helperPath,
		pythonExe:  pythonExe,
		modelPath:  modelPath,
		modelName:  modelName,
	}
}

// Start spawns the helper subprocess and waits for the startup ack.
// Idempotent — calling twice is a no-op.
func (c *LocalClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd != nil {
		return nil
	}

	cmd := exec.Command(c.pythonExe, c.helperPath, "--model-path", c.modelPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("local embed: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("local embed: stdout pipe: %w", err)
	}
	// Forward stderr to our stderr (helper writes load progress there)
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("local embed: start helper: %w", err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReader(stdout)

	// Wait for startup ack (with timeout)
	startCtx, cancel := context.WithTimeout(ctx, localStartupTimeout)
	defer cancel()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := c.stdout.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("local embed: helper startup read: %w", r.err)
		}
		var ack struct {
			ID    string `json:"id"`
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(r.line), &ack); err != nil {
			return fmt.Errorf("local embed: helper startup parse: %w (raw: %q)", err, r.line)
		}
		if ack.Error != "" {
			return fmt.Errorf("local embed: helper startup error: %s", ack.Error)
		}
		if ack.ID != "__startup__" || !ack.OK {
			return fmt.Errorf("local embed: unexpected startup ack: %s", r.line)
		}
		return nil
	case <-startCtx.Done():
		return errors.New("local embed: helper did not signal ready within timeout")
	}
}

// Close stops the helper.
func (c *LocalClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return nil
	}
	_ = c.stdin.Close()
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
	c.cmd = nil
	return nil
}

// Model returns the human-readable model name (for logging).
func (c *LocalClient) Model() string {
	return c.modelName
}

// Embed implements the Embedder interface (mirrors CohereClient.Embed).
// inputType is currently ignored — bge-m3 doesn't distinguish query from
// document at the embedding step. (Future: prepend "query:" / "passage:"
// for E5-style models.)
func (c *LocalClient) Embed(ctx context.Context, texts []string, _ InputType) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == nil {
		return nil, errors.New("local embed: helper not started — call Start() first")
	}

	id := fmt.Sprintf("r%d", c.idCount.Add(1))
	req := map[string]any{"id": id, "texts": texts}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("local embed: marshal req: %w", err)
	}
	if _, err := c.stdin.Write(append(reqBytes, '\n')); err != nil {
		return nil, fmt.Errorf("local embed: write: %w", err)
	}

	// Read response (with timeout)
	callCtx, cancel := context.WithTimeout(ctx, localCallTimeout)
	defer cancel()
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := c.stdout.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf("local embed: read response: %w", r.err)
		}
		var resp struct {
			ID         string      `json:"id"`
			Embeddings [][]float32 `json:"embeddings"`
			Error      string      `json:"error"`
		}
		if err := json.Unmarshal([]byte(r.line), &resp); err != nil {
			return nil, fmt.Errorf("local embed: parse response: %w (raw: %q)", err, r.line)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("local embed helper: %s", resp.Error)
		}
		if resp.ID != id {
			return nil, fmt.Errorf("local embed: id mismatch (sent %s, got %s)", id, resp.ID)
		}
		return resp.Embeddings, nil
	case <-callCtx.Done():
		return nil, errors.New("local embed: helper did not respond within timeout")
	}
}
