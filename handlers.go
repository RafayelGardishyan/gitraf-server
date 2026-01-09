package main

import (
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
)

// Server holds the application state
type Server struct {
	reposPath  string
	publicURL  string
	tailnetURL string
	templates  *template.Template
}

// NewServer creates a new Server instance
func NewServer(reposPath, publicURL, tailnetURL, templatesPath string) (*Server, error) {
	// Parse all templates
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"truncate": func(s string, n int) string {
			if len(s) <= n {
				return s
			}
			return s[:n] + "..."
		},
		"split": func(s, sep string) []string {
			return strings.Split(s, sep)
		},
		"add": func(a, b int) int {
			return a + b
		},
		"formatSize": func(size int64) string {
			if size < 1024 {
				return fmt.Sprintf("%d B", size)
			}
			kb := float64(size) / 1024
			if kb < 1024 {
				return fmt.Sprintf("%.1f KB", kb)
			}
			mb := kb / 1024
			return fmt.Sprintf("%.1f MB", mb)
		},
		"isText": func(name string) bool {
			textExts := []string{
				".txt", ".md", ".go", ".py", ".js", ".ts", ".html", ".css",
				".json", ".yaml", ".yml", ".xml", ".sh", ".bash", ".zsh",
				".c", ".h", ".cpp", ".hpp", ".java", ".rs", ".rb", ".php",
				".sql", ".toml", ".ini", ".cfg", ".conf", ".dockerfile",
				".gitignore", ".env", ".mod", ".sum", ".lock",
			}
			ext := strings.ToLower(filepath.Ext(name))
			for _, e := range textExts {
				if ext == e {
					return true
				}
			}
			// Check common files without extension
			lowerName := strings.ToLower(name)
			noExtFiles := []string{
				"readme", "license", "makefile", "dockerfile", "gemfile",
				"rakefile", "procfile", "vagrantfile",
			}
			for _, f := range noExtFiles {
				if lowerName == f {
					return true
				}
			}
			return false
		},
		"getExt": func(name string) string {
			ext := filepath.Ext(name)
			if ext != "" {
				return strings.TrimPrefix(ext, ".")
			}
			return ""
		},
		"firstLine": func(s string) string {
			lines := strings.Split(s, "\n")
			if len(lines) > 0 {
				return lines[0]
			}
			return s
		},
		"pathJoin": func(parts ...string) string {
			return filepath.Join(parts...)
		},
	}).ParseGlob(filepath.Join(templatesPath, "*.html"))
	if err != nil {
		return nil, err
	}

	return &Server{
		reposPath:  reposPath,
		publicURL:  publicURL,
		tailnetURL: tailnetURL,
		templates:  tmpl,
	}, nil
}

// isTailnetRequest checks if the request comes from the tailnet
func (s *Server) isTailnetRequest(r *http.Request) bool {
	clientIP := GetClientIP(
		r.RemoteAddr,
		r.Header.Get("X-Forwarded-For"),
		r.Header.Get("X-Real-IP"),
	)
	return IsTailnetIP(clientIP)
}

// renderTemplate renders a template with the given data
func (s *Server) renderTemplate(w http.ResponseWriter, name string, data interface{}) {
	err := s.templates.ExecuteTemplate(w, name, data)
	if err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

// handleIndex shows the list of repositories
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	showPrivate := s.isTailnetRequest(r)

	repos, err := ListRepos(s.reposPath, showPrivate)
	if err != nil {
		log.Printf("Error listing repos: %v", err)
		http.Error(w, "Error listing repositories", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":       "Repositories",
		"Repos":       repos,
		"IsTailnet":   showPrivate,
		"PublicURL":   s.publicURL,
		"TailnetURL":  s.tailnetURL,
	}

	s.renderTemplate(w, "index.html", data)
}

// handleRepo shows a repository's file tree
func (s *Server) handleRepo(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

	if !RepoExists(s.reposPath, repoName) {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Check access
	repoPath := filepath.Join(s.reposPath, repoName+".git")
	if !IsPublicRepo(repoPath) && !s.isTailnetRequest(r) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Get default branch
	defaultBranch, err := GetDefaultBranch(repoPath)
	if err != nil {
		defaultBranch = "main"
	}

	// Redirect to tree view with default branch
	http.Redirect(w, r, "/"+repoName+"/tree/"+defaultBranch+"/", http.StatusFound)
}

// handleTree shows the file tree for a specific path
func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	path := chi.URLParam(r, "*")

	if !RepoExists(s.reposPath, repoName) {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Check access
	repoPath := filepath.Join(s.reposPath, repoName+".git")
	if !IsPublicRepo(repoPath) && !s.isTailnetRequest(r) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	entries, err := GetTree(s.reposPath, repoName, ref, path)
	if err != nil {
		log.Printf("Error getting tree: %v", err)
		http.Error(w, "Error reading repository", http.StatusInternalServerError)
		return
	}

	// Get branches for dropdown
	branches, _ := GetBranches(s.reposPath, repoName)

	// Build breadcrumbs
	var breadcrumbs []map[string]string
	if path != "" {
		parts := strings.Split(strings.Trim(path, "/"), "/")
		currentPath := ""
		for _, part := range parts {
			if part == "" {
				continue
			}
			currentPath = filepath.Join(currentPath, part)
			breadcrumbs = append(breadcrumbs, map[string]string{
				"Name": part,
				"Path": currentPath,
			})
		}
	}

	data := map[string]interface{}{
		"Title":       repoName,
		"RepoName":    repoName,
		"Ref":         ref,
		"Path":        path,
		"Entries":     entries,
		"Branches":    branches,
		"Breadcrumbs": breadcrumbs,
		"IsTailnet":   s.isTailnetRequest(r),
		"IsPublic":    IsPublicRepo(repoPath),
		"PublicURL":   s.publicURL,
		"TailnetURL":  s.tailnetURL,
	}

	s.renderTemplate(w, "repo.html", data)
}

// handleBlob shows the content of a file
func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")
	path := chi.URLParam(r, "*")

	if !RepoExists(s.reposPath, repoName) {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Check access
	repoPath := filepath.Join(s.reposPath, repoName+".git")
	if !IsPublicRepo(repoPath) && !s.isTailnetRequest(r) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	content, err := GetBlob(s.reposPath, repoName, ref, path)
	if err != nil {
		log.Printf("Error getting blob: %v", err)
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	// Get branches for dropdown
	branches, _ := GetBranches(s.reposPath, repoName)

	// Build breadcrumbs
	var breadcrumbs []map[string]string
	parts := strings.Split(strings.Trim(path, "/"), "/")
	currentPath := ""
	for i, part := range parts {
		if part == "" {
			continue
		}
		currentPath = filepath.Join(currentPath, part)
		// Last item is the file itself
		if i == len(parts)-1 {
			breadcrumbs = append(breadcrumbs, map[string]string{
				"Name":   part,
				"Path":   "",
				"IsFile": "true",
			})
		} else {
			breadcrumbs = append(breadcrumbs, map[string]string{
				"Name": part,
				"Path": currentPath,
			})
		}
	}

	fileName := filepath.Base(path)

	data := map[string]interface{}{
		"Title":       fileName + " - " + repoName,
		"RepoName":    repoName,
		"Ref":         ref,
		"Path":        path,
		"FileName":    fileName,
		"Content":     string(content),
		"Branches":    branches,
		"Breadcrumbs": breadcrumbs,
		"IsTailnet":   s.isTailnetRequest(r),
		"IsPublic":    IsPublicRepo(repoPath),
		"PublicURL":   s.publicURL,
		"TailnetURL":  s.tailnetURL,
	}

	s.renderTemplate(w, "blob.html", data)
}

// handleCommits shows the commit history
func (s *Server) handleCommits(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	ref := chi.URLParam(r, "ref")

	if !RepoExists(s.reposPath, repoName) {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Check access
	repoPath := filepath.Join(s.reposPath, repoName+".git")
	if !IsPublicRepo(repoPath) && !s.isTailnetRequest(r) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	commits, err := GetCommits(s.reposPath, repoName, ref, 50)
	if err != nil {
		log.Printf("Error getting commits: %v", err)
		http.Error(w, "Error reading commits", http.StatusInternalServerError)
		return
	}

	// Get branches for dropdown
	branches, _ := GetBranches(s.reposPath, repoName)

	data := map[string]interface{}{
		"Title":      "Commits - " + repoName,
		"RepoName":   repoName,
		"Ref":        ref,
		"Commits":    commits,
		"Branches":   branches,
		"IsTailnet":  s.isTailnetRequest(r),
		"IsPublic":   IsPublicRepo(repoPath),
		"PublicURL":  s.publicURL,
		"TailnetURL": s.tailnetURL,
	}

	s.renderTemplate(w, "commits.html", data)
}
