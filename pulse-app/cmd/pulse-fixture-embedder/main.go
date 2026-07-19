// Command pulse-fixture-embedder is a native, deterministic protocol fixture.
// It is built only by native packed-product tests and is never a production
// embedding model or release artifact.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
)

const dimensions = 1024

type request struct {
	ID     string   `json:"id"`
	Schema string   `json:"schema"`
	Texts  []string `json:"texts"`
}

type ready struct {
	Dimensions int    `json:"dimensions"`
	ID         string `json:"id"`
	Model      string `json:"model"`
	Normalized bool   `json:"normalized"`
	OK         bool   `json:"ok"`
	Pooling    string `json:"pooling"`
	Protocol   int    `json:"protocol"`
	Schema     string `json:"schema"`
}

type response struct {
	Embeddings [][]float64 `json:"embeddings"`
	ID         string      `json:"id"`
	Schema     string      `json:"schema"`
}

func vector(text string) []float64 {
	digest := sha256.Sum256([]byte(text))
	index := (int(digest[0])<<8 | int(digest[1])) % dimensions
	values := make([]float64, dimensions)
	values[index] = 1
	return values
}

func main() {
	if len(os.Args) != 5 || os.Args[1] != "--model-root" || os.Args[3] != "--support-root" {
		fmt.Fprintln(os.Stderr, "pulse-fixture-embedder: invalid arguments")
		os.Exit(2)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(ready{
		Dimensions: dimensions, ID: "__startup__", Model: "bge-m3", Normalized: true,
		OK: true, Pooling: "cls", Protocol: 1, Schema: "pulse.embedder.ready.v1",
	}); err != nil {
		os.Exit(1)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var input request
		if err := json.Unmarshal(scanner.Bytes(), &input); err != nil ||
			input.Schema != "pulse.embedder.request.v1" || input.ID == "" ||
			len(input.Texts) < 1 || len(input.Texts) > 96 {
			fmt.Fprintln(os.Stderr, "pulse-fixture-embedder: invalid request")
			os.Exit(1)
		}
		vectors := make([][]float64, len(input.Texts))
		for index, text := range input.Texts {
			if text == "" {
				fmt.Fprintln(os.Stderr, "pulse-fixture-embedder: empty text")
				os.Exit(1)
			}
			vectors[index] = vector(text)
		}
		if err := encoder.Encode(response{
			Embeddings: vectors, ID: input.ID, Schema: "pulse.embedder.response.v1",
		}); err != nil {
			os.Exit(1)
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "pulse-fixture-embedder: input failed")
		os.Exit(1)
	}
}
