package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
)

// Server holds the application state
type Server struct {
	reposPath  string
	publicURL  string
	tailnetURL string
	templates  *template.Template
	markdown   goldmark.Markdown
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

	// Create markdown parser with extensions
	md := goldmark.New(
		goldmark.WithExtensions(
			extension.GFM, // GitHub Flavored Markdown
			extension.Table,
			extension.Strikethrough,
			extension.TaskList,
		),
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(),
		),
		goldmark.WithRendererOptions(
			html.WithHardWraps(),
			html.WithUnsafe(), // Allow raw HTML in markdown
		),
	)

	return &Server{
		reposPath:  reposPath,
		publicURL:  publicURL,
		tailnetURL: tailnetURL,
		templates:  tmpl,
		markdown:   md,
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

// renderMarkdown converts markdown to HTML
func (s *Server) renderMarkdown(source []byte) template.HTML {
	var buf bytes.Buffer
	if err := s.markdown.Convert(source, &buf); err != nil {
		return template.HTML("<p>Error rendering markdown</p>")
	}
	return template.HTML(buf.String())
}

// findReadme looks for a README file in the given tree entries
func findReadme(entries []TreeEntry) string {
	readmeNames := []string{
		"README.md", "readme.md", "Readme.md",
		"README.MD", "README", "readme",
		"README.txt", "readme.txt",
		"README.rst", "readme.rst",
	}
	for _, name := range readmeNames {
		for _, entry := range entries {
			if !entry.IsDir && entry.Name == name {
				return entry.Name
			}
		}
	}
	return ""
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

	// Check if repository is empty
	if IsEmptyRepo(repoPath) {
		data := map[string]interface{}{
			"Title":      repoName,
			"RepoName":   repoName,
			"IsEmpty":    true,
			"IsTailnet":  s.isTailnetRequest(r),
			"IsPublic":   IsPublicRepo(repoPath),
			"PublicURL":  s.publicURL,
			"TailnetURL": s.tailnetURL,
		}
		s.renderTemplate(w, "repo.html", data)
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

	// Look for README file in current directory
	var readmeHTML template.HTML
	var readmeName string
	if readme := findReadme(entries); readme != "" {
		readmeName = readme
		readmePath := readme
		if path != "" && path != "/" {
			readmePath = filepath.Join(strings.Trim(path, "/"), readme)
		}
		if content, err := GetBlob(s.reposPath, repoName, ref, readmePath); err == nil {
			// Check if it's a markdown file
			if strings.HasSuffix(strings.ToLower(readme), ".md") {
				readmeHTML = s.renderMarkdown(content)
			} else {
				// For non-markdown README files, show as preformatted text
				readmeHTML = template.HTML("<pre>" + template.HTMLEscapeString(string(content)) + "</pre>")
			}
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
		"ReadmeHTML":  readmeHTML,
		"ReadmeName":  readmeName,
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

// handleDocs shows the documentation page
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	section := chi.URLParam(r, "section")
	if section == "" {
		section = "overview"
	}

	// Valid sections
	validSections := map[string]string{
		"overview":       "Overview",
		"installation":   "Installation",
		"cli":            "CLI Reference",
		"server":         "Server Setup",
		"pages":          "Pages Hosting",
		"lfs":            "Git LFS",
		"troubleshooting": "Troubleshooting",
	}

	sectionTitle, ok := validSections[section]
	if !ok {
		section = "overview"
		sectionTitle = "Overview"
	}

	data := map[string]interface{}{
		"Title":         "Documentation - " + sectionTitle,
		"Section":       section,
		"SectionTitle":  sectionTitle,
		"Sections":      validSections,
		"IsTailnet":     s.isTailnetRequest(r),
		"PublicURL":     s.publicURL,
		"TailnetURL":    s.tailnetURL,
	}

	s.renderTemplate(w, "docs.html", data)
}

// handleNewRepo shows the new repository form (tailnet only)
func (s *Server) handleNewRepo(w http.ResponseWriter, r *http.Request) {
	if !s.isTailnetRequest(r) {
		http.Error(w, "Access denied - Tailnet required", http.StatusForbidden)
		return
	}

	data := map[string]interface{}{
		"Title":      "New Repository",
		"IsTailnet":  true,
		"PublicURL":  s.publicURL,
		"TailnetURL": s.tailnetURL,
	}

	s.renderTemplate(w, "new-repo.html", data)
}

// handleNewRepoPost creates a new repository (tailnet only)
func (s *Server) handleNewRepoPost(w http.ResponseWriter, r *http.Request) {
	if !s.isTailnetRequest(r) {
		http.Error(w, "Access denied - Tailnet required", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	repoName := strings.TrimSpace(r.FormValue("name"))
	description := strings.TrimSpace(r.FormValue("description"))
	isPublic := r.FormValue("visibility") == "public"

	// Validate repo name
	if repoName == "" {
		http.Error(w, "Repository name is required", http.StatusBadRequest)
		return
	}

	// Check for invalid characters
	for _, c := range repoName {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.') {
			http.Error(w, "Invalid repository name - only alphanumeric, dash, underscore, and dot allowed", http.StatusBadRequest)
			return
		}
	}

	// Check if repo already exists
	if RepoExists(s.reposPath, repoName) {
		http.Error(w, "Repository already exists", http.StatusConflict)
		return
	}

	// Create the bare repository
	repoPath := filepath.Join(s.reposPath, repoName+".git")
	if err := CreateBareRepo(repoPath); err != nil {
		log.Printf("Error creating repository: %v", err)
		http.Error(w, "Failed to create repository", http.StatusInternalServerError)
		return
	}

	// Set description
	if description != "" {
		descPath := filepath.Join(repoPath, "description")
		os.WriteFile(descPath, []byte(description), 0644)
	}

	// Set visibility
	if isPublic {
		exportPath := filepath.Join(repoPath, "git-daemon-export-ok")
		os.WriteFile(exportPath, []byte{}, 0644)
	}

	// Redirect to the new repo
	http.Redirect(w, r, "/"+repoName, http.StatusFound)
}

// handleRepoSettings shows repository settings (tailnet only)
func (s *Server) handleRepoSettings(w http.ResponseWriter, r *http.Request) {
	if !s.isTailnetRequest(r) {
		http.Error(w, "Access denied - Tailnet required", http.StatusForbidden)
		return
	}

	repoName := chi.URLParam(r, "repo")

	if !RepoExists(s.reposPath, repoName) {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	repoPath := filepath.Join(s.reposPath, repoName+".git")

	// Read current description
	description := ""
	if data, err := os.ReadFile(filepath.Join(repoPath, "description")); err == nil {
		description = strings.TrimSpace(string(data))
		// Ignore default git description
		if strings.HasPrefix(description, "Unnamed repository") {
			description = ""
		}
	}

	// Read pages config if exists
	pagesEnabled := false
	pagesBranch := "main"
	pagesBuildCmd := ""
	pagesOutputDir := "public"
	pagesConfigPath := filepath.Join(repoPath, "git-pages.json")
	if data, err := os.ReadFile(pagesConfigPath); err == nil {
		var pagesConfig map[string]interface{}
		if json.Unmarshal(data, &pagesConfig) == nil {
			// Check for explicit enabled field, or assume enabled if file exists
			if enabled, ok := pagesConfig["enabled"].(bool); ok {
				pagesEnabled = enabled
			} else {
				pagesEnabled = true
			}
			if b, ok := pagesConfig["branch"].(string); ok {
				pagesBranch = b
			}
			if b, ok := pagesConfig["build_command"].(string); ok {
				pagesBuildCmd = b
			}
			if d, ok := pagesConfig["output_dir"].(string); ok {
				pagesOutputDir = d
			}
		}
	}

	// Read mirror config if exists
	mirrorEnabled := false
	mirrorURL := ""
	mirrorConfigPath := filepath.Join(repoPath, "git-mirror.json")
	if data, err := os.ReadFile(mirrorConfigPath); err == nil {
		var mirrorConfig map[string]interface{}
		if json.Unmarshal(data, &mirrorConfig) == nil {
			if enabled, ok := mirrorConfig["enabled"].(bool); ok {
				mirrorEnabled = enabled
			}
			if url, ok := mirrorConfig["github_url"].(string); ok {
				mirrorURL = url
			}
		}
	}

	// Get branches for dropdown
	branches, _ := GetBranches(s.reposPath, repoName)

	data := map[string]interface{}{
		"Title":          repoName + " Settings",
		"RepoName":       repoName,
		"Description":    description,
		"IsPublic":       IsPublicRepo(repoPath),
		"IsTailnet":      true,
		"PublicURL":      s.publicURL,
		"TailnetURL":     s.tailnetURL,
		"PagesEnabled":   pagesEnabled,
		"PagesBranch":    pagesBranch,
		"PagesBuildCmd":  pagesBuildCmd,
		"PagesOutputDir": pagesOutputDir,
		"MirrorEnabled":  mirrorEnabled,
		"MirrorURL":      mirrorURL,
		"Branches":       branches,
	}

	s.renderTemplate(w, "settings.html", data)
}

// handleRepoSettingsPost saves repository settings (tailnet only)
func (s *Server) handleRepoSettingsPost(w http.ResponseWriter, r *http.Request) {
	if !s.isTailnetRequest(r) {
		http.Error(w, "Access denied - Tailnet required", http.StatusForbidden)
		return
	}

	repoName := chi.URLParam(r, "repo")

	if !RepoExists(s.reposPath, repoName) {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	repoPath := filepath.Join(s.reposPath, repoName+".git")

	// Update description
	description := strings.TrimSpace(r.FormValue("description"))
	descPath := filepath.Join(repoPath, "description")
	os.WriteFile(descPath, []byte(description), 0644)

	// Update visibility
	isPublic := r.FormValue("visibility") == "public"
	exportPath := filepath.Join(repoPath, "git-daemon-export-ok")
	if isPublic {
		os.WriteFile(exportPath, []byte{}, 0644)
	} else {
		os.Remove(exportPath)
	}

	// Update pages config
	pagesEnabled := r.FormValue("pages_enabled") == "on"
	pagesConfigPath := filepath.Join(repoPath, "git-pages.json")
	pagesBranch := r.FormValue("pages_branch")
	if pagesBranch == "" {
		pagesBranch = "main"
	}
	pagesOutputDir := r.FormValue("pages_output_dir")
	if pagesOutputDir == "" {
		pagesOutputDir = "public"
	}
	pagesConfig := map[string]interface{}{
		"enabled":       pagesEnabled,
		"branch":        pagesBranch,
		"build_command": r.FormValue("pages_build_cmd"),
		"output_dir":    pagesOutputDir,
	}
	pagesData, _ := json.MarshalIndent(pagesConfig, "", "  ")
	os.WriteFile(pagesConfigPath, pagesData, 0644)

	// Update mirror config
	mirrorEnabled := r.FormValue("mirror_enabled") == "on"
	mirrorURL := strings.TrimSpace(r.FormValue("mirror_url"))
	mirrorConfigPath := filepath.Join(repoPath, "git-mirror.json")
	if mirrorEnabled && mirrorURL != "" {
		mirrorConfig := map[string]interface{}{
			"enabled":    true,
			"github_url": mirrorURL,
		}
		mirrorData, _ := json.MarshalIndent(mirrorConfig, "", "  ")
		os.WriteFile(mirrorConfigPath, mirrorData, 0644)
	} else if !mirrorEnabled {
		// Set enabled to false instead of deleting
		mirrorConfig := map[string]interface{}{
			"enabled":    false,
			"github_url": mirrorURL,
		}
		mirrorData, _ := json.MarshalIndent(mirrorConfig, "", "  ")
		os.WriteFile(mirrorConfigPath, mirrorData, 0644)
	}

	// Redirect back to repo
	http.Redirect(w, r, "/"+repoName, http.StatusFound)
}

// handleCommit shows a specific commit with its diff
func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")
	hash := chi.URLParam(r, "hash")

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

	commitDiff, err := GetCommitDiff(s.reposPath, repoName, hash)
	if err != nil {
		log.Printf("Error getting commit diff: %v", err)
		http.Error(w, "Commit not found", http.StatusNotFound)
		return
	}

	// Get branches for context
	branches, _ := GetBranches(s.reposPath, repoName)
	defaultBranch := "main"
	if len(branches) > 0 {
		defaultBranch = branches[0]
	}

	data := map[string]interface{}{
		"Title":         "Commit " + commitDiff.Commit.ShortHash + " - " + repoName,
		"RepoName":      repoName,
		"Ref":           defaultBranch,
		"CommitDiff":    commitDiff,
		"Branches":      branches,
		"IsTailnet":     s.isTailnetRequest(r),
		"IsPublic":      IsPublicRepo(repoPath),
		"PublicURL":     s.publicURL,
		"TailnetURL":    s.tailnetURL,
	}

	s.renderTemplate(w, "commit.html", data)
}

// handleRobots returns robots.txt that disallows all crawlers
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(`User-agent: *
Disallow: /
`))
}
