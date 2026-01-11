package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"os/exec"
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
	reposPath    string
	publicURL    string
	tailnetURL   string
	pagesBaseURL string
	templates    *template.Template
	markdown     goldmark.Markdown
}

// NewServer creates a new Server instance
func NewServer(reposPath, publicURL, tailnetURL, templatesPath, pagesBaseURL string) (*Server, error) {
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
		reposPath:    reposPath,
		publicURL:    publicURL,
		tailnetURL:   tailnetURL,
		pagesBaseURL: pagesBaseURL,
		templates:    tmpl,
		markdown:     md,
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

	// Get submodule info for this path
	submodules, _ := GetSubmodulesForPath(s.reposPath, repoName, ref, path)

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

	// Check if pages is enabled for this repo
	pagesEnabled := false
	pagesURL := ""
	pagesConfigPath := filepath.Join(repoPath, "git-pages.json")
	if data, err := os.ReadFile(pagesConfigPath); err == nil {
		var pagesConfig map[string]interface{}
		if json.Unmarshal(data, &pagesConfig) == nil {
			if enabled, ok := pagesConfig["enabled"].(bool); ok {
				pagesEnabled = enabled
			} else {
				pagesEnabled = true // File exists means enabled
			}
		}
	}
	if pagesEnabled && s.pagesBaseURL != "" {
		pagesURL = "https://" + repoName + "." + s.pagesBaseURL
	}

	data := map[string]interface{}{
		"Title":        repoName,
		"RepoName":     repoName,
		"Ref":          ref,
		"Path":         path,
		"Entries":      entries,
		"Submodules":   submodules,
		"Branches":     branches,
		"Breadcrumbs":  breadcrumbs,
		"IsTailnet":    s.isTailnetRequest(r),
		"IsPublic":     IsPublicRepo(repoPath),
		"PublicURL":    s.publicURL,
		"TailnetURL":   s.tailnetURL,
		"ReadmeHTML":   readmeHTML,
		"ReadmeName":   readmeName,
		"PagesEnabled": pagesEnabled,
		"PagesURL":     pagesURL,
	}

	s.renderTemplate(w, "repo.html", data)
}

// handleSubmodule shows details for a specific submodule
func (s *Server) handleSubmodule(w http.ResponseWriter, r *http.Request) {
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

	info, err := GetSubmoduleInfo(s.reposPath, repoName, ref, path)
	if err != nil {
		log.Printf("Error getting submodule info: %v", err)
		http.Error(w, "Submodule not found", http.StatusNotFound)
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
		isLast := i == len(parts)-1
		bc := map[string]string{
			"Name": part,
			"Path": currentPath,
		}
		if isLast {
			bc["IsSubmodule"] = "true"
		}
		breadcrumbs = append(breadcrumbs, bc)
	}

	data := map[string]interface{}{
		"Title":       info.Name + " (submodule) - " + repoName,
		"RepoName":    repoName,
		"Ref":         ref,
		"Path":        path,
		"Submodule":   info,
		"Branches":    branches,
		"Breadcrumbs": breadcrumbs,
		"IsTailnet":   s.isTailnetRequest(r),
		"IsPublic":    IsPublicRepo(repoPath),
		"PublicURL":   s.publicURL,
		"TailnetURL":  s.tailnetURL,
	}

	s.renderTemplate(w, "submodule.html", data)
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

	// Get SSH key info for mirroring
	sshKeyExists := sshKeyExists()
	var sshPublicKey, sshKeyFingerprint string
	if sshKeyExists {
		sshPublicKey, _ = getSSHPublicKey()
		sshKeyFingerprint, _ = getSSHKeyFingerprint()
	}

	// Read LFS config (server-level)
	lfsEnabled := false
	lfsEndpoint := ""
	lfsBucket := ""
	lfsRegion := "auto"
	lfsAccessKey := ""
	lfsSecretKey := ""
	lfsConfigPath := filepath.Join(filepath.Dir(s.reposPath), "lfs-config.json")
	if data, err := os.ReadFile(lfsConfigPath); err == nil {
		var lfsConfig map[string]interface{}
		if json.Unmarshal(data, &lfsConfig) == nil {
			lfsEnabled = true
			if v, ok := lfsConfig["endpoint"].(string); ok {
				lfsEndpoint = v
			}
			if v, ok := lfsConfig["bucket"].(string); ok {
				lfsBucket = v
			}
			if v, ok := lfsConfig["region"].(string); ok {
				lfsRegion = v
			}
			if v, ok := lfsConfig["access_key"].(string); ok {
				lfsAccessKey = v
			}
			if v, ok := lfsConfig["secret_key"].(string); ok {
				lfsSecretKey = v
			}
		}
	}

	// Read Backup config (server-level)
	backupEnabled := false
	backupEndpoint := ""
	backupBucket := ""
	backupRegion := "auto"
	backupAccessKey := ""
	backupSecretKey := ""
	backupSchedule := "daily"
	backupConfigPath := filepath.Join(filepath.Dir(s.reposPath), "backup-config.json")
	if data, err := os.ReadFile(backupConfigPath); err == nil {
		var backupConfig map[string]interface{}
		if json.Unmarshal(data, &backupConfig) == nil {
			if v, ok := backupConfig["enabled"].(bool); ok {
				backupEnabled = v
			}
			if v, ok := backupConfig["endpoint"].(string); ok {
				backupEndpoint = v
			}
			if v, ok := backupConfig["bucket"].(string); ok {
				backupBucket = v
			}
			if v, ok := backupConfig["region"].(string); ok {
				backupRegion = v
			}
			if v, ok := backupConfig["access_key"].(string); ok {
				backupAccessKey = v
			}
			if v, ok := backupConfig["secret_key"].(string); ok {
				backupSecretKey = v
			}
			if v, ok := backupConfig["schedule"].(string); ok {
				backupSchedule = v
			}
		}
	}

	data := map[string]interface{}{
		"Title":             repoName + " Settings",
		"RepoName":          repoName,
		"Description":       description,
		"IsPublic":          IsPublicRepo(repoPath),
		"IsTailnet":         true,
		"PublicURL":         s.publicURL,
		"TailnetURL":        s.tailnetURL,
		"PagesEnabled":      pagesEnabled,
		"PagesBranch":       pagesBranch,
		"PagesBuildCmd":     pagesBuildCmd,
		"PagesOutputDir":    pagesOutputDir,
		"MirrorEnabled":     mirrorEnabled,
		"MirrorURL":         mirrorURL,
		"Branches":          branches,
		"SSHKeyExists":      sshKeyExists,
		"SSHPublicKey":      sshPublicKey,
		"SSHKeyFingerprint": sshKeyFingerprint,
		// LFS config
		"LFSEnabled":   lfsEnabled,
		"LFSEndpoint":  lfsEndpoint,
		"LFSBucket":    lfsBucket,
		"LFSRegion":    lfsRegion,
		"LFSAccessKey": lfsAccessKey,
		"LFSSecretKey": lfsSecretKey,
		// Backup config
		"BackupEnabled":   backupEnabled,
		"BackupEndpoint":  backupEndpoint,
		"BackupBucket":    backupBucket,
		"BackupRegion":    backupRegion,
		"BackupAccessKey": backupAccessKey,
		"BackupSecretKey": backupSecretKey,
		"BackupSchedule":  backupSchedule,
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

// SSH Key Management for GitHub Mirroring

// getSSHKeyPath returns the path to the SSH key for mirroring
func getSSHKeyPath() string {
	return "/opt/ogit/config/ssh/id_ed25519"
}

// sshKeyExists checks if the SSH key exists
func sshKeyExists() bool {
	_, err := os.Stat(getSSHKeyPath())
	return err == nil
}

// generateSSHKey generates a new Ed25519 SSH key for mirroring
func generateSSHKey() error {
	keyPath := getSSHKeyPath()

	// Create directory if it doesn't exist
	keyDir := filepath.Dir(keyPath)
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return fmt.Errorf("failed to create ssh directory: %v", err)
	}

	// Generate key using ssh-keygen
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-f", keyPath, "-N", "", "-C", "gitraf-mirror")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("ssh-keygen failed: %v - %s", err, string(output))
	}

	return nil
}

// getSSHPublicKey reads and returns the public key
func getSSHPublicKey() (string, error) {
	data, err := os.ReadFile(getSSHKeyPath() + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// getSSHKeyFingerprint returns the key fingerprint
func getSSHKeyFingerprint() (string, error) {
	cmd := exec.Command("ssh-keygen", "-lf", getSSHKeyPath()+".pub")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// handleGenerateSSHKey generates a new SSH key for mirroring (tailnet only)
func (s *Server) handleGenerateSSHKey(w http.ResponseWriter, r *http.Request) {
	if !s.isTailnetRequest(r) {
		http.Error(w, "Access denied - Tailnet required", http.StatusForbidden)
		return
	}

	// Check if key already exists
	if sshKeyExists() {
		http.Error(w, "SSH key already exists. Delete the existing key first to regenerate.", http.StatusConflict)
		return
	}

	// Generate the key
	if err := generateSSHKey(); err != nil {
		log.Printf("Error generating SSH key: %v", err)
		http.Error(w, "Failed to generate SSH key", http.StatusInternalServerError)
		return
	}

	// Get the referrer to redirect back
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	}

	http.Redirect(w, r, referer, http.StatusFound)
}

// handleUpdateServer updates the gitraf-server binary (tailnet only)
func (s *Server) handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !s.isTailnetRequest(r) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"status":"error","message":"Access denied - Tailnet required"}`))
		return
	}

	serverDir := "/opt/gitraf-server"
	goPath := "/usr/local/go/bin/go"

	// Pull latest changes
	log.Printf("Pulling latest changes from git...")
	cmd := exec.Command("git", "pull", "origin", "main")
	cmd.Dir = serverDir
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("Error pulling updates: %v - %s", err, string(output))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{"status":"error","message":"Failed to pull updates: %s"}`, strings.ReplaceAll(string(output), `"`, `\"`))))
		return
	}

	// Build the new binary
	log.Printf("Building new binary...")
	cmd = exec.Command(goPath, "build", "-o", "gitraf-server", ".")
	cmd.Dir = serverDir
	if output, err := cmd.CombinedOutput(); err != nil {
		log.Printf("Error building: %v - %s", err, string(output))
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(fmt.Sprintf(`{"status":"error","message":"Failed to build: %s"}`, strings.ReplaceAll(string(output), `"`, `\"`))))
		return
	}

	log.Printf("Update built successfully. Restarting service...")

	// Send response before restarting
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"success","message":"Update complete. Restarting service..."}`))

	// Restart in background after a short delay
	go func() {
		// Small delay to allow response to be sent
		cmd := exec.Command("sleep", "1")
		cmd.Run()

		// Restart the service
		cmd = exec.Command("systemctl", "restart", "gitraf-server")
		if err := cmd.Run(); err != nil {
			log.Printf("systemctl restart failed: %v, server may need manual restart", err)
		}
	}()
}

// handleLFSConfigPost saves the LFS configuration (tailnet only)
func (s *Server) handleLFSConfigPost(w http.ResponseWriter, r *http.Request) {
	if !s.isTailnetRequest(r) {
		http.Error(w, "Access denied - Tailnet required", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	lfsEnabled := r.FormValue("lfs_enabled") == "on"
	lfsConfigPath := filepath.Join(filepath.Dir(s.reposPath), "lfs-config.json")

	if lfsEnabled {
		// Save LFS config
		lfsConfig := map[string]interface{}{
			"endpoint":   strings.TrimSpace(r.FormValue("lfs_endpoint")),
			"bucket":     strings.TrimSpace(r.FormValue("lfs_bucket")),
			"region":     strings.TrimSpace(r.FormValue("lfs_region")),
			"access_key": strings.TrimSpace(r.FormValue("lfs_access_key")),
			"secret_key": strings.TrimSpace(r.FormValue("lfs_secret_key")),
		}

		// Set default region if empty
		if lfsConfig["region"] == "" {
			lfsConfig["region"] = "auto"
		}

		data, err := json.MarshalIndent(lfsConfig, "", "  ")
		if err != nil {
			log.Printf("Error marshaling LFS config: %v", err)
			http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
			return
		}

		if err := os.WriteFile(lfsConfigPath, data, 0600); err != nil {
			log.Printf("Error writing LFS config: %v", err)
			http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
			return
		}

		log.Printf("LFS configuration saved to %s", lfsConfigPath)
	} else {
		// Remove LFS config file when disabled
		os.Remove(lfsConfigPath)
		log.Printf("LFS configuration disabled, removed %s", lfsConfigPath)
	}

	// Redirect back to referrer
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	}
	http.Redirect(w, r, referer, http.StatusFound)
}

// replaceConfigLine replaces a shell config line like KEY="value" with a new value
func replaceConfigLine(content, key, newValue string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, key+"=") {
			lines[i] = key + "=" + newValue
			return strings.Join(lines, "\n")
		}
	}
	return content
}

// handleBackupConfigPost saves the backup configuration (tailnet only)
func (s *Server) handleBackupConfigPost(w http.ResponseWriter, r *http.Request) {
	if !s.isTailnetRequest(r) {
		http.Error(w, "Access denied - Tailnet required", http.StatusForbidden)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Invalid form data", http.StatusBadRequest)
		return
	}

	backupEnabled := r.FormValue("backup_enabled") == "on"
	backupConfigPath := filepath.Join(filepath.Dir(s.reposPath), "backup-config.json")

	// Save backup config (always save, just toggle enabled flag)
	backupConfig := map[string]interface{}{
		"enabled":    backupEnabled,
		"endpoint":   strings.TrimSpace(r.FormValue("backup_endpoint")),
		"bucket":     strings.TrimSpace(r.FormValue("backup_bucket")),
		"region":     strings.TrimSpace(r.FormValue("backup_region")),
		"access_key": strings.TrimSpace(r.FormValue("backup_access_key")),
		"secret_key": strings.TrimSpace(r.FormValue("backup_secret_key")),
		"schedule":   r.FormValue("backup_schedule"),
	}

	// Set defaults
	if backupConfig["region"] == "" {
		backupConfig["region"] = "auto"
	}
	if backupConfig["schedule"] == "" {
		backupConfig["schedule"] = "daily"
	}

	data, err := json.MarshalIndent(backupConfig, "", "  ")
	if err != nil {
		log.Printf("Error marshaling backup config: %v", err)
		http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
		return
	}

	if err := os.WriteFile(backupConfigPath, data, 0600); err != nil {
		log.Printf("Error writing backup config: %v", err)
		http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
		return
	}

	log.Printf("Backup configuration saved to %s (enabled: %v)", backupConfigPath, backupEnabled)

	// Also update the backup shell script config file
	backupShellConfigPath := filepath.Join(filepath.Dir(s.reposPath), "..", "backup", "backup.conf")
	if _, err := os.Stat(backupShellConfigPath); err == nil {
		// Read existing config
		existingData, err := os.ReadFile(backupShellConfigPath)
		if err == nil {
			content := string(existingData)
			endpoint := backupConfig["endpoint"].(string)
			bucket := backupConfig["bucket"].(string)
			accessKey := backupConfig["access_key"].(string)
			secretKey := backupConfig["secret_key"].(string)

			// Update AWS_ENDPOINT_URL
			if endpoint != "" {
				content = replaceConfigLine(content, "AWS_ENDPOINT_URL", fmt.Sprintf(`"%s"`, endpoint))
			}
			// Update S3_BUCKET
			if bucket != "" {
				content = replaceConfigLine(content, "S3_BUCKET", fmt.Sprintf(`"%s"`, bucket))
			}

			// Write AWS credentials file for the default profile
			awsCredsDir := filepath.Join(os.Getenv("HOME"), ".aws")
			if awsCredsDir == "/.aws" {
				awsCredsDir = "/root/.aws"
			}
			if accessKey != "" && secretKey != "" {
				os.MkdirAll(awsCredsDir, 0700)
				credsContent := fmt.Sprintf("[default]\naws_access_key_id = %s\naws_secret_access_key = %s\n", accessKey, secretKey)
				if err := os.WriteFile(filepath.Join(awsCredsDir, "credentials"), []byte(credsContent), 0600); err != nil {
					log.Printf("Warning: Could not write AWS credentials: %v", err)
				}
			}

			if err := os.WriteFile(backupShellConfigPath, []byte(content), 0644); err != nil {
				log.Printf("Warning: Could not update backup.conf: %v", err)
			} else {
				log.Printf("Backup shell config updated at %s", backupShellConfigPath)
			}
		}
	}

	// Redirect back to referrer
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	}
	http.Redirect(w, r, referer, http.StatusFound)
}
