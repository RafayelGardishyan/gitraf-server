package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

func main() {
	// Parse command line flags
	reposPath := flag.String("repos", "", "Path to git repositories directory")
	port := flag.Int("port", 8080, "Port to listen on")
	tlsPort := flag.Int("tls-port", 0, "TLS port to listen on (0 to disable)")
	tlsCert := flag.String("tls-cert", "", "Path to TLS certificate")
	tlsKey := flag.String("tls-key", "", "Path to TLS private key")
	publicURL := flag.String("public-url", "", "Public URL for HTTPS clone")
	tailnetURL := flag.String("tailnet-url", "", "Tailnet URL for SSH clone")
	templatesPath := flag.String("templates", "", "Path to templates directory (defaults to ./templates)")
	pagesBaseURL := flag.String("pages-base-url", "", "Base URL for gitraf-pages (e.g., example.com for {repo}.example.com)")
	flag.Parse()

	// Check environment variables as fallbacks
	if *reposPath == "" {
		*reposPath = os.Getenv("GITRAF_REPOS_PATH")
	}
	if os.Getenv("GITRAF_PORT") != "" {
		fmt.Sscanf(os.Getenv("GITRAF_PORT"), "%d", port)
	}
	if *publicURL == "" {
		*publicURL = os.Getenv("GITRAF_PUBLIC_URL")
	}
	if *tailnetURL == "" {
		*tailnetURL = os.Getenv("GITRAF_TAILNET_URL")
	}
	if *pagesBaseURL == "" {
		*pagesBaseURL = os.Getenv("GITRAF_PAGES_BASE_URL")
	}

	// Validate required parameters
	if *reposPath == "" {
		log.Fatal("Error: repos path is required. Use --repos flag or GITRAF_REPOS_PATH environment variable")
	}

	// Check if repos directory exists
	if _, err := os.Stat(*reposPath); os.IsNotExist(err) {
		log.Fatalf("Error: repos directory does not exist: %s", *reposPath)
	}

	// Determine templates path
	if *templatesPath == "" {
		// Try to find templates relative to executable
		execPath, err := os.Executable()
		if err == nil {
			execDir := filepath.Dir(execPath)
			candidatePath := filepath.Join(execDir, "templates")
			if _, err := os.Stat(candidatePath); err == nil {
				*templatesPath = candidatePath
			}
		}
		// Fall back to current directory
		if *templatesPath == "" {
			*templatesPath = "templates"
		}
	}

	// Check if templates directory exists
	if _, err := os.Stat(*templatesPath); os.IsNotExist(err) {
		log.Fatalf("Error: templates directory does not exist: %s", *templatesPath)
	}

	// Create server
	server, err := NewServer(*reposPath, *publicURL, *tailnetURL, *templatesPath, *pagesBaseURL)
	if err != nil {
		log.Fatalf("Error creating server: %v", err)
	}

	// Create router
	r := chi.NewRouter()

	// Middleware
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// Routes
	r.Get("/robots.txt", server.handleRobots)
	r.Get("/", server.handleIndex)
	r.Get("/docs", server.handleDocs)
	r.Get("/docs/{section}", server.handleDocs)
	r.Get("/new", server.handleNewRepo)
	r.Post("/new", server.handleNewRepoPost)
	r.Get("/{repo}", server.handleRepo)
	r.Get("/{repo}/tree/{ref}/*", server.handleTree)
	r.Get("/{repo}/blob/{ref}/*", server.handleBlob)
	r.Get("/{repo}/submodule/{ref}/*", server.handleSubmodule)
	r.Get("/{repo}/commits/{ref}", server.handleCommits)
	r.Get("/{repo}/commit/{hash}", server.handleCommit)
	r.Get("/{repo}/settings", server.handleRepoSettings)
	r.Post("/{repo}/settings", server.handleRepoSettingsPost)

	// Admin routes (tailnet only)
	r.Post("/admin/generate-ssh-key", server.handleGenerateSSHKey)
	r.Post("/admin/update-server", server.handleUpdateServer)

	// Git LFS routes
	r.Post("/{repo}.git/info/lfs/objects/batch", server.handleLFSBatch)
	r.Post("/{repo}.git/info/lfs/locks/verify", server.handleLFSLocksVerify)
	r.Get("/{repo}.git/info/lfs/locks", server.handleLFSLocks)

	// Start server
	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Starting gitraf-server on %s", addr)
	log.Printf("Repos path: %s", *reposPath)
	log.Printf("Templates path: %s", *templatesPath)
	if *publicURL != "" {
		log.Printf("Public URL: %s", *publicURL)
	}
	if *tailnetURL != "" {
		log.Printf("Tailnet URL: %s", *tailnetURL)
	}
	if *pagesBaseURL != "" {
		log.Printf("Pages base URL: %s", *pagesBaseURL)
	}

	// Start TLS server if configured
	if *tlsPort > 0 && *tlsCert != "" && *tlsKey != "" {
		tlsAddr := fmt.Sprintf(":%d", *tlsPort)
		log.Printf("Starting TLS server on %s", tlsAddr)
		go func() {
			if err := http.ListenAndServeTLS(tlsAddr, *tlsCert, *tlsKey, r); err != nil {
				log.Printf("TLS server error: %v", err)
			}
		}()
	}

	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("Error starting server: %v", err)
	}
}
