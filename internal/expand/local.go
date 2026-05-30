// Local query-expansion client — wraps mlx_query_expand_helper.py
// (a long-running Python subprocess) and serves a single Expand(query)
// → []string call over JSON-on-stdio.
//
// Used as an opt-in pre-step before BM25 in the retrieval engine: a
// query like «release plan» gets expanded with canonical graph
// terms («milestone», «Q3 release plan», ...) so lexical search can
// hit events the embedding rounds off. Expansion is grounded: the
// helper loads the user's actual entities from Pulse SQLite at
// startup and limits the model to that vocabulary.
//
// Pattern mirrors embed.LocalClient — see internal/embed/local.go.
package expand

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
	startupTimeout = 120 * time.Second // model load
	callTimeout    = 30 * time.Second  // per-query
)

// Expander is the abstract interface so the engine can ignore the
// transport. Stub returns empty slice → engine falls back to pure
// hybrid (B) without expansion.
type Expander interface {
	Expand(ctx context.Context, query string) ([]string, error)
}

// LocalClient runs the helper as a subprocess and serializes calls.
type LocalClient struct {
	pythonExe  string
	helperPath string
	dbPath     string
	modelPath  string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	idCount atomic.Uint64
	started bool
}

func NewLocal(pythonExe, helperPath, dbPath, modelPath string) *LocalClient {
	return &LocalClient{
		pythonExe:  pythonExe,
		helperPath: helperPath,
		dbPath:     dbPath,
		modelPath:  modelPath,
	}
}

// Start spawns the helper and waits for the startup ack. Idempotent.
func (c *LocalClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}
	args := []string{c.helperPath, "--db", c.dbPath, "--model-path", c.modelPath}
	cmd := exec.Command(c.pythonExe, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("expand: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("expand: stdout pipe: %w", err)
	}
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("expand: spawn helper: %w", err)
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = bufio.NewReader(stdout)

	startCtx, cancel := context.WithTimeout(ctx, startupTimeout)
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
			return fmt.Errorf("expand: helper startup read: %w", r.err)
		}
		var ack struct {
			ID        string `json:"id"`
			OK        bool   `json:"ok"`
			VocabSize int    `json:"vocab_size"`
			Error     string `json:"error"`
		}
		if err := json.Unmarshal([]byte(r.line), &ack); err != nil {
			return fmt.Errorf("expand: parse startup ack: %w (raw: %q)", err, r.line)
		}
		if ack.Error != "" {
			return fmt.Errorf("expand: helper startup error: %s", ack.Error)
		}
		if ack.ID != "__startup__" || !ack.OK {
			return fmt.Errorf("expand: unexpected startup ack: %s", r.line)
		}
		c.started = true
		return nil
	case <-startCtx.Done():
		return errors.New("expand: helper did not signal ready within timeout")
	}
}

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
	c.started = false
	return nil
}

// Expand sends a query and returns the helper's expansion list.
// Caller-side timeout via ctx; helper-side hard cap via callTimeout.
func (c *LocalClient) Expand(ctx context.Context, query string) ([]string, error) {
	if query == "" {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return nil, errors.New("expand: helper not started")
	}
	id := fmt.Sprintf("e%d", c.idCount.Add(1))
	req := map[string]any{"id": id, "query": query}
	reqBytes, _ := json.Marshal(req)
	if _, err := c.stdin.Write(append(reqBytes, '\n')); err != nil {
		return nil, fmt.Errorf("expand: write: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, callTimeout)
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
			return nil, fmt.Errorf("expand: read response: %w", r.err)
		}
		var resp struct {
			ID         string   `json:"id"`
			Expansions []string `json:"expansions"`
			Error      string   `json:"error"`
		}
		if err := json.Unmarshal([]byte(r.line), &resp); err != nil {
			return nil, fmt.Errorf("expand: parse response: %w (raw: %q)", err, r.line)
		}
		if resp.Error != "" {
			return nil, fmt.Errorf("expand helper: %s", resp.Error)
		}
		if resp.ID != id {
			return nil, fmt.Errorf("expand: id mismatch (sent %s got %s)", id, resp.ID)
		}
		return resp.Expansions, nil
	case <-callCtx.Done():
		return nil, errors.New("expand: helper did not respond within timeout")
	}
}
