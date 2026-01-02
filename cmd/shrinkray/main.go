package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	shrinkray "github.com/gwlsn/shrinkray"
	"github.com/gwlsn/shrinkray/internal/api"
	"github.com/gwlsn/shrinkray/internal/auth"
	"github.com/gwlsn/shrinkray/internal/auth/oidc"
	"github.com/gwlsn/shrinkray/internal/auth/password"
	"github.com/gwlsn/shrinkray/internal/browse"
	"github.com/gwlsn/shrinkray/internal/config"
	"github.com/gwlsn/shrinkray/internal/ffmpeg"
	"github.com/gwlsn/shrinkray/internal/jobs"
)

func main() {
	// Parse command line flags
	configPath := flag.String("config", "", "Path to config file (default: ./config/shrinkray.yaml)")
	port := flag.Int("port", 8080, "Port to listen on")
	mediaPath := flag.String("media", "", "Override media path from config")
	debugUI := flag.Bool("debug", false, "Use debug UI instead of production UI")
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
	if cfg.TempPath != "" {
		fmt.Printf("  Temp path:    %s\n", cfg.TempPath)
	} else {
		fmt.Printf("  Temp path:    (same as source)\n")
	}
	fmt.Printf("  Workers:      %d\n", cfg.Workers)
	fmt.Printf("  Original:     %s\n", cfg.OriginalHandling)
	fmt.Printf("  Subtitles:    %s\n", cfg.SubtitleHandling)
	fmt.Println()

	// Check ffmpeg/ffprobe availability
	if err := checkFFmpeg(cfg); err != nil {
		log.Fatalf("FFmpeg check failed: %v", err)
	}

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
	browser.SetHideProcessingTmp(cfg.HideProcessingTmp)

	queue, err := jobs.NewQueue(cfg.QueueFile)
	if err != nil {
		log.Fatalf("Failed to initialize job queue: %v", err)
	}

	workerPool := jobs.NewWorkerPool(queue, cfg, browser.InvalidateCache)

	// Create API handler
	handler := api.NewHandler(browser, queue, workerPool, cfg, cfgPath)

	authRegistry := auth.NewRegistry()
	authRegistry.Register("noop", auth.NewNoopProvider())

	providerName := cfg.Auth.Provider
	if providerName == "" {
		providerName = "noop"
	}

	var authMiddleware *auth.Middleware
	if cfg.Auth.Enabled {
		if providerName == "password" {
			passwordProvider, err := password.NewProvider(cfg.Auth.Password.Users, cfg.Auth.Password.HashAlgo, cfg.Auth.Secret)
			if err != nil {
				log.Fatalf("Failed to initialize password auth: %v", err)
			}
			authRegistry.Register("password", passwordProvider)
		}
		if providerName == "oidc" {
			oidcProvider, err := oidc.NewProvider(
				context.Background(),
				cfg.Auth.OIDC.Issuer,
				cfg.Auth.OIDC.ClientID,
				cfg.Auth.OIDC.ClientSecret,
				cfg.Auth.OIDC.RedirectURL,
				cfg.Auth.OIDC.Scopes,
				cfg.Auth.OIDC.GroupClaim,
				cfg.Auth.OIDC.AllowedGroups,
				cfg.Auth.Secret,
			)
			if err != nil {
				log.Fatalf("Failed to initialize oidc auth: %v", err)
			}
			authRegistry.Register("oidc", oidcProvider)
		}

		authProvider, ok := authRegistry.Provider(providerName)
		if !ok {
			log.Fatalf("Unknown auth provider: %s", providerName)
		}

		bypassPaths := append(auth.DefaultBypassPaths(), cfg.Auth.BypassPaths...)
		authMiddleware = auth.NewMiddleware(authProvider, bypassPaths)
	}

	router := api.NewRouter(handler, shrinkray.WebFS, *debugUI, authMiddleware)

	// Start worker pool
	workerPool.Start()
	defer workerPool.Stop()

	uiMode := "production"
	if *debugUI {
		uiMode = "debug"
	}
	fmt.Printf("  UI mode:      %s\n", uiMode)
	fmt.Printf("  Starting server on port %d\n", *port)
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
	fmt.Printf("  FFmpeg:       %s\n", cfg.FFmpegPath)
	fmt.Printf("  FFprobe:      %s\n", cfg.FFprobePath)
	return nil
}
