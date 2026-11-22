// Command clipper runs the local auto-clipping studio.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/blaze-techs/Clippers-Live-Hub/internal/api"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/config"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/ffmpeg"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/pipeline"
	"github.com/blaze-techs/Clippers-Live-Hub/internal/webui"
)

var version = "dev"

func main() {
	var (
		port    = flag.Int("port", 8765, "local port for the control UI")
		outDir  = flag.String("out", "", "directory for exported clips")
		dataDir = flag.String("data", "", "directory for projects and the media library")
		cfgPath = flag.String("config", "", "path to a JSON config file")
		open    = flag.Bool("open", true, "open the UI in the default browser")
		host    = flag.String("host", "127.0.0.1", "interface to bind")
		ver     = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *ver {
		fmt.Printf("Clippers Live Hub %s (%s/%s)\n", version, runtime.GOOS, runtime.GOARCH)
		return
	}

	baseDir := defaultBaseDir()
	cfg := config.Default(baseDir)
	if *cfgPath != "" {
		loaded, err := config.Load(*cfgPath)
		if err != nil && !os.IsNotExist(err) {
			log.Fatalf("config: %v", err)
		}
		cfg = loaded
	}
	if *outDir != "" {
		cfg.OutputDir = *outDir
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}
	if *port > 0 {
		cfg.Port = *port
	}
	cfg.Normalise()
	if err := os.MkdirAll(cfg.OutputDir, 0o755); err != nil {
		log.Fatalf("create output dir: %v", err)
	}

	tools, err := ffmpeg.Locate(cfg.FFmpegPath, cfg.FFprobePath)
	if err != nil {
		log.Fatalf("ffmpeg required: %v", err)
	}

	pipe := pipeline.New(cfg, tools)
	srv := api.New(cfg, pipe, tools, webui.FS())

	studio, err := api.NewStudio(cfg, tools)
	if err != nil {
		log.Fatalf("project store: %v", err)
	}
	srv.SetStudio(studio)

	addr := net.JoinHostPort(*host, fmt.Sprintf("%d", cfg.Port))
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen %s: %v", addr, err)
	}
	url := fmt.Sprintf("http://%s/", addr)

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("Clippers Live Hub %s", version)
	log.Printf("ffmpeg: %s", tools.FFmpeg)
	log.Printf("clips:  %s", cfg.OutputDir)
	log.Printf("ui:     %s", url)

	if *open {
		go func() {
			time.Sleep(400 * time.Millisecond)
			openBrowser(url)
		}()
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		log.Printf("shutting down")
		pipe.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(ctx)
	}()

	if err := httpSrv.Serve(listener); err != nil && err != http.ErrServerClosed {
		log.Fatalf("serve: %v", err)
	}
}

func defaultBaseDir() string {
	if home := os.Getenv("CLIPPERS_HOME"); home != "" {
		return home
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if writable(dir) {
			return dir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "ClippersLiveHub")
	}
	return "."
}

func writable(dir string) bool {
	probe := filepath.Join(dir, ".write-test")
	f, err := os.Create(probe)
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(probe)
	return true
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		log.Printf("open browser: %v (visit %s)", err, url)
	}
}
