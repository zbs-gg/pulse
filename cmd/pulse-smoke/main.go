package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nkkmnk/pulse/internal/model"
	"github.com/nkkmnk/pulse/internal/providers/doinference"
	"github.com/nkkmnk/pulse/internal/providers/openaicompat"
)

func main() {
	alias := flag.String("model", "", "model alias from registry (default: registry default)")
	prompt := flag.String("prompt", "respond with ok", "prompt to send")
	configPath := flag.String("config", "", "path to models.json (default: PULSE_MODELS_PATH or built-in default)")
	flag.Parse()

	var (
		reg *model.Registry
		err error
	)
	if *configPath != "" {
		reg, err = model.LoadRegistryFromFile(*configPath)
	} else {
		reg, err = model.LoadRegistry("")
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "registry: %v\n", err)
		os.Exit(2)
	}
	router := model.NewRouter(reg, map[string]model.Provider{
		model.ProviderDOInference:  doinference.New(),
		model.ProviderOpenAICompat: openaicompat.New(),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	resp, err := router.Chat(ctx, model.ChatRequest{
		Alias:    *alias,
		Messages: []model.Message{{Role: "user", Content: *prompt}},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "chat: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("provider=%s model=%s\n%s\n", resp.Provider, resp.Model, resp.Text)
}
