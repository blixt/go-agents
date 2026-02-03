package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/flitsinc/go-agents/internal/agenttools"
	"github.com/flitsinc/go-agents/internal/ai"
	"github.com/flitsinc/go-agents/internal/api"
	"github.com/flitsinc/go-agents/internal/config"
	"github.com/flitsinc/go-agents/internal/engine"
	"github.com/flitsinc/go-agents/internal/eventbus"
	agentctx "github.com/flitsinc/go-agents/internal/prompt"
	"github.com/flitsinc/go-agents/internal/state"
	"github.com/flitsinc/go-agents/internal/tasks"
	"github.com/flitsinc/go-agents/internal/web"
)

func main() {
	cfg := config.Load()
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	if err := os.MkdirAll(cfg.SnapshotDir, 0o755); err != nil {
		log.Fatalf("create snapshot dir: %v", err)
	}

	db, err := state.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := state.NewStore(db)
	bus := eventbus.NewBus(db)
	manager := tasks.NewManager(db, bus)
	execTool := agenttools.ExecTool(manager)
	rt := engine.NewRuntime(bus, manager, nil)

	var llmClient *ai.Client
	if cfg.LLMModel != "" && cfg.LLMAPIKey != "" {
		llmClient, err = ai.NewClient(ai.Config{
			Provider: cfg.LLMProvider,
			Model:    cfg.LLMModel,
			APIKey:   cfg.LLMAPIKey,
		}, execTool)
		if err != nil {
			log.Printf("LLM disabled: %v", err)
		}
	}

	if llmClient != nil {
		rt.LLM = llmClient
		if rt.Context != nil {
			rt.Context.Compactor = agentctx.NewLLMCompactor(llmClient)
		}
	}

	_ = llmClient // reserved for future runtime wiring.

	listener, err := engine.ListenerFromEnv()
	if err != nil {
		log.Fatalf("listener: %v", err)
	}
	if listener == nil {
		listener, err = net.Listen("tcp", cfg.HTTPAddr)
		if err != nil {
			log.Fatalf("listen: %v", err)
		}
	}

	var httpServer *http.Server
	serverCtx, serverCancel := context.WithCancel(context.Background())

	restarter := &engine.Restarter{
		Listener: listener,
		Args:     os.Args,
		Env:      os.Environ(),
	}
	restartFn := func() error {
		if err := restarter.Restart(); err != nil {
			return err
		}
		go func() {
			time.Sleep(750 * time.Millisecond)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = httpServer.Shutdown(ctx)
			os.Exit(0)
		}()
		return nil
	}

	apiServer := &api.Server{Tasks: manager, Bus: bus, Store: store, Runtime: rt, Restart: restartFn, RestartToken: cfg.RestartToken}
	webServer := &web.Server{Dir: cfg.WebDir}

	mux := http.NewServeMux()
	mux.Handle("/api/", apiServer.Handler())
	mux.Handle("/", webServer.Handler())

	httpServer = &http.Server{
		Handler:           loggingMiddleware(mux),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(_ net.Listener) context.Context {
			return serverCtx
		},
	}

	go func() {
		log.Printf("agentd listening on %s", listener.Addr())
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	serverCancel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("server shutdown error: %v", err)
	}
	_ = httpServer.Close()
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
