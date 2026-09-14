package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"vshell/internal/config"
	"vshell/internal/core"
	"vshell/internal/store"
	"vshell/internal/webserver"
)

func main() {
	var runPath string
	flag.StringVar(&runPath, "run-path", "", "directory containing conf, db, plugins and web")
	flag.Parse()
	runPath = config.ResolveRunPath(runPath)
	cfg, err := config.Load(filepath.Join(runPath, "conf", "setting.conf"))
	if err != nil {
		log.Fatal(err)
	}
	statePath := filepath.Join(runPath, "db", "vshell-state.json")
	st, err := store.Open(statePath)
	if err != nil {
		log.Fatal(err)
	}
	c := core.New(st)
	defer c.Close()
	c.StartSaved()
	s := webserver.New(cfg, runPath, c, st)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.Shutdown(shutdown)
	}()
	log.Printf("vShell server listening on %s:%d", cfg.WebIP, cfg.WebPort)
	if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
