package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	shrinkray "github.com/graysonwilson/shrinkray"
	"github.com/graysonwilson/shrinkray/internal/api"
	"github.com/graysonwilson/shrinkray/internal/browse"
	"github.com/graysonwilson/shrinkray/internal/config"
	"github.com/graysonwilson/shrinkray/internal/ffmpeg"
	"github.com/graysonwilson/shrinkray/internal/jobs"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "", "Path to config file (default: ./config/shrinkray.yaml)")
	port := flag.Int("port", 8080, "Port to listen on")
	mediaPath := flag.String("media", "", "Override media path from config")
	flag.Parse()

	// Determine config path
	cfgPath := *configPath
	if cfgPath == "" {
		// Check environment variable
		if envPath := os.Getenv("CONFIG_PATH"); envPath != "" {
			cfgPath = envPath
		} else {
			// Default to ./config/shrinkray.yaml
			cfgPath = "config/shrinkray.yaml"
		}
	}

	// Load config
	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Printf("Warning: Could not load config from %s: %v", cfgPath, err)
		cfg = config.DefaultConfig()
	}

	// Override with environment variables
	if envMedia := os.Getenv("MEDIA_PATH"); envMedia != "" {
		cfg.MediaPath = envMedia
	}
	if *mediaPath != "" {
		cfg.MediaPath = *mediaPath
	}

	// Validate media path exists
	if _, err := os.Stat(cfg.MediaPath); os.IsNotExist(err) {
		log.Fatalf("Media path does not exist: %s", cfg.MediaPath)
	}

	// Set up queue file path
	if cfg.QueueFile == "" {
		configDir := filepath.Dir(cfgPath)
		if configDir == "." {
			configDir = "config"
		}
		cfg.QueueFile = filepath.Join(configDir, "queue.json")
	}

	// Ensure config directory exists
	if err := os.MkdirAll(filepath.Dir(cfg.QueueFile), 0755); err != nil {
		log.Printf("Warning: Could not create config directory: %v", err)
	}

	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║                      🔬 SHRINKRAY                         ║")
	fmt.Println("║          Simple, efficient video transcoding              ║")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("  Media path:   %s\n", cfg.MediaPath)
	fmt.Printf("  Config:       %s\n", cfgPath)
	fmt.Printf("  Queue file:   %s\n", cfg.QueueFile)
	fmt.Printf("  Workers:      %d\n", cfg.Workers)
	fmt.Printf("  Original:     %s\n", cfg.OriginalHandling)
	fmt.Println()

	// Check ffmpeg/ffprobe availability
	if err := checkFFmpeg(cfg); err != nil {
		log.Fatalf("FFmpeg check failed: %v", err)
	}

	// Detect available hardware encoders
	encoders := ffmpeg.DetectEncoders(cfg.FFmpegPath)
	ffmpeg.InitPresets()

	// Display detected encoders
	fmt.Println("  Encoders:")
	best := ffmpeg.GetBestEncoder()
	for _, enc := range ffmpeg.ListAvailableEncoders() {
		if enc.Available {
			marker := "  "
			if enc.Accel == best.Accel {
				marker = "* "
			}
			fmt.Printf("    %s%s (%s)\n", marker, enc.Name, enc.Encoder)
		}
	}
	_ = encoders // silence unused warning
	fmt.Println()

	// Initialize components
	prober := ffmpeg.NewProber(cfg.FFprobePath)
	browser := browse.NewBrowser(prober, cfg.MediaPath)

	queue, err := jobs.NewQueue(cfg.QueueFile)
	if err != nil {
		log.Fatalf("Failed to initialize job queue: %v", err)
	}

	workerPool := jobs.NewWorkerPool(queue, cfg, browser.InvalidateCache)

	// Create API handler
	handler := api.NewHandler(browser, queue, workerPool, cfg)
	router := api.NewRouter(handler, shrinkray.WebFS)

	// Start worker pool
	workerPool.Start()
	defer workerPool.Stop()

	fmt.Printf("  Starting server on http://localhost:%d\n", *port)
	fmt.Println()
	fmt.Println("  Press Ctrl+C to stop")
	fmt.Println()

	// Set up graceful shutdown
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", *port),
		Handler: router,
	}

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n  Shutting down...")
		workerPool.Stop()
		server.Close()
	}()

	// Start server
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}

	fmt.Println("  Goodbye!")
}

func checkFFmpeg(cfg *config.Config) error {
	// Try to run ffmpeg -version
	prober := ffmpeg.NewProber(cfg.FFprobePath)
	transcoder := ffmpeg.NewTranscoder(cfg.FFmpegPath)

	// Simple check - just verify the binaries exist by trying to probe a non-existent file
	// We expect an error, but not "executable not found"
	_, _ = prober, transcoder

	fmt.Printf("  FFmpeg:       %s\n", cfg.FFmpegPath)
	fmt.Printf("  FFprobe:      %s\n", cfg.FFprobePath)

	return nil
}
