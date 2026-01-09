package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	shrinkray "github.com/gwlsn/shrinkray"
	"github.com/gwlsn/shrinkray/internal/api"
	"github.com/gwlsn/shrinkray/internal/browse"
	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/jobs"
	"github.com/gwlsn/shrinkray/internal/logger"
	"github.com/gwlsn/shrinkray/internal/store"
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
		// Initialize logger with default level for this warning
		logger.Init("info")
		logger.Warn("Could not load config", "path", cfgPath, "error", err)
		cfg = config.DefaultConfig()
	}

	// Initialize logger with configured level
	logger.Init(cfg.LogLevel)

	// Override with environment variables
	if envMedia := os.Getenv("MEDIA_PATH"); envMedia != "" {
		cfg.MediaPath = envMedia
	}
	if *mediaPath != "" {
		cfg.MediaPath = *mediaPath
	}

	// Override temp path with environment variable
	if envTemp := os.Getenv("TEMP_PATH"); envTemp != "" {
		cfg.TempPath = envTemp
	}

	// Auto-detect /temp mount if temp_path is still not configured
	if cfg.TempPath == "" {
		if info, err := os.Stat("/temp"); err == nil && info.IsDir() {
			cfg.TempPath = "/temp"
		}
	}

	// Validate media path exists
	if _, err := os.Stat(cfg.MediaPath); os.IsNotExist(err) {
		logger.Error("Media path does not exist", "path", cfg.MediaPath)
		os.Exit(1)
	}

	// Determine config directory for data storage
	configDir := filepath.Dir(cfgPath)
	if configDir == "." {
		configDir = "config"
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		logger.Warn("Could not create config directory", "error", err)
	}

	// Initialize SQLite store (handles migration from JSON if needed)
	jobStore, err := store.InitStore(configDir)
	if err != nil {
		logger.Error("Failed to initialize job store", "error", err)
		os.Exit(1)
	}
	defer jobStore.Close()

	fmt.Println("╔═══════════════════════════════════════════════════════════╗")
	fmt.Println("║                         SHRINKRAY                         ║")
	fmt.Println("║          Simple, user-friendly video transcoding          ║")
	versionLine := fmt.Sprintf("v%s", shrinkray.Version)
	padding := 59 - len(versionLine)
	fmt.Printf("║%*s%s%*s║\n", padding/2, "", versionLine, (padding+1)/2, "")
	fmt.Println("╚═══════════════════════════════════════════════════════════╝")
	fmt.Println()
	fmt.Printf("  Media path:   %s\n", cfg.MediaPath)
	fmt.Printf("  Config:       %s\n", cfgPath)
	fmt.Printf("  Database:     %s\n", jobStore.Path())
	if cfg.TempPath != "" {
		fmt.Printf("  Temp path:    %s\n", cfg.TempPath)
	} else {
		fmt.Printf("  Temp path:    (same as source)\n")
	}
	fmt.Printf("  Workers:      %d\n", cfg.Workers)
	fmt.Printf("  Original:     %s\n", cfg.OriginalHandling)
	fmt.Printf("  FFmpeg:       %s\n", cfg.FFmpegPath)
	fmt.Printf("  FFprobe:      %s\n", cfg.FFprobePath)
	fmt.Println()

	// Detect available hardware encoders
	ffmpeg.DetectEncoders(cfg.FFmpegPath)
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
	fmt.Println()

	// Initialize components
	prober := ffmpeg.NewProber(cfg.FFprobePath)
	browser := browse.NewBrowser(prober, cfg.MediaPath)

	queue, err := jobs.NewQueueWithStore(jobStore)
	if err != nil {
		logger.Error("Failed to initialize job queue", "error", err)
		jobStore.Close()
		os.Exit(1) //nolint:gocritic // store closed explicitly above
	}

	workerPool := jobs.NewWorkerPool(queue, cfg, browser.InvalidateCache)

	// Create API handler
	handler := api.NewHandler(browser, queue, workerPool, cfg, cfgPath)
	handler.SetStore(jobStore) // Enable session/lifetime stats
	router := api.NewRouter(handler, shrinkray.WebFS)

	// Start worker pool
	workerPool.Start()

	fmt.Printf("  Starting server on port %d\n", *port)
	fmt.Println()
	fmt.Println("  Press Ctrl+C to stop")
	fmt.Println()

	// Print logging separator and consolidated startup log
	fmt.Println("─────────────────────────────────────────────────────────────")
	fmt.Printf("  Logging started (level: %s)\n", cfg.LogLevel)
	fmt.Println("─────────────────────────────────────────────────────────────")
	logger.Info("Shrinkray started", "version", shrinkray.Version, "encoder", best.Name, "workers", cfg.Workers, "port", *port)

	// Set up graceful shutdown
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", *port),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\n  Shutting down...")
		logger.Info("Shutdown signal received")
		workerPool.Stop()
		server.Close()
	}()

	// Start server
	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Error("Server error", "error", err)
		workerPool.Stop()
		os.Exit(1)
	}

	logger.Info("Server stopped")
	fmt.Println("  Goodbye!")
}
