package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// Repo represents a git repository
type Repo struct {
	Name        string
	Description string
	IsPublic    bool
	LastCommit  time.Time
}

// TreeEntry represents a file or directory in a git tree
type TreeEntry struct {
	Name        string
	IsDir       bool
	IsSubmodule bool
	Mode        string
	Size        int64
	Hash        string
}

// SubmoduleInfo represents parsed submodule information
type SubmoduleInfo struct {
	Name      string // Submodule name from .gitmodules
	Path      string // Path relative to repo root
	URL       string // Clone URL
	Branch    string // Tracking branch (optional)
	Hash      string // Current commit hash (40 chars)
	ShortHash string // Short hash for display (8 chars)
	Status    string // "configured", "missing-config"
	WebURL    string // Constructed web URL for external link
}

// Commit represents a git commit
type Commit struct {
	Hash      string
	ShortHash string
	Message   string
	Author    string
	Email     string
	Date      time.Time
}

// CommitDiff represents a commit with its diff
type CommitDiff struct {
	Commit     Commit
	ParentHash string
	Files      []FileDiff
	Stats      DiffStats
}

// FileDiff represents changes to a single file
type FileDiff struct {
	Name      string
	OldName   string // For renames
	Status    string // added, modified, deleted, renamed
	Additions int
	Deletions int
	IsBinary  bool
	Chunks    []DiffChunk
}

// DiffChunk represents a chunk of changes in a file
type DiffChunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []DiffLine
}

// DiffLine represents a single line in a diff
type DiffLine struct {
	Type    string // context, add, delete
	Content string
	OldNum  int
	NewNum  int
}

// DiffStats represents overall diff statistics
type DiffStats struct {
	FilesChanged int
	Additions    int
	Deletions    int
}

// ListRepos returns a list of repositories in the given path
func ListRepos(reposPath string, showPrivate bool) ([]Repo, error) {
	entries, err := os.ReadDir(reposPath)
	if err != nil {
		return nil, err
	}

	var repos []Repo
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Only consider directories ending with .git
		if !strings.HasSuffix(name, ".git") {
			continue
		}

		repoPath := filepath.Join(reposPath, name)
		isPublic := IsPublicRepo(repoPath)

		// Skip private repos if not showing private
		if !showPrivate && !isPublic {
			continue
		}

		repo := Repo{
			Name:     strings.TrimSuffix(name, ".git"),
			IsPublic: isPublic,
		}

		// Try to get last commit time and use commit message as description
		if r, err := git.PlainOpen(repoPath); err == nil {
			if head, err := r.Head(); err == nil {
				if commit, err := r.CommitObject(head.Hash()); err == nil {
					repo.LastCommit = commit.Author.When
					// Use first line of commit message as description
					msg := strings.TrimSpace(commit.Message)
					if idx := strings.Index(msg, "\n"); idx != -1 {
						msg = msg[:idx]
					}
					repo.Description = msg
				}
			}
		}

		repos = append(repos, repo)
	}

	// Sort by last commit time, most recent first
	sort.Slice(repos, func(i, j int) bool {
		return repos[i].LastCommit.After(repos[j].LastCommit)
	})

	return repos, nil
}

// IsPublicRepo checks if a repository has git-daemon-export-ok file
func IsPublicRepo(repoPath string) bool {
	exportOkPath := filepath.Join(repoPath, "git-daemon-export-ok")
	_, err := os.Stat(exportOkPath)
	return err == nil
}

// GetDefaultBranch returns the default branch (HEAD target) for a repository
func GetDefaultBranch(repoPath string) (string, error) {
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", err
	}

	head, err := r.Head()
	if err != nil {
		return "", err
	}

	return head.Name().Short(), nil
}

// GetTree returns the tree entries for a given path in a repository
func GetTree(reposPath, repoName, ref, path string) ([]TreeEntry, error) {
	repoPath := filepath.Join(reposPath, repoName+".git")
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}

	// Resolve the reference
	hash, err := resolveRef(r, ref)
	if err != nil {
		return nil, err
	}

	commit, err := r.CommitObject(hash)
	if err != nil {
		return nil, err
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}

	// Navigate to the requested path
	if path != "" && path != "/" {
		path = strings.Trim(path, "/")
		tree, err = tree.Tree(path)
		if err != nil {
			return nil, err
		}
	}

	var entries []TreeEntry
	for _, e := range tree.Entries {
		entry := TreeEntry{
			Name:        e.Name,
			IsDir:       e.Mode == filemode.Dir,
			IsSubmodule: e.Mode == filemode.Submodule,
			Mode:        e.Mode.String(),
			Hash:        e.Hash.String(),
		}

		// Get file size if it's a file
		if e.Mode.IsFile() {
			if file, err := tree.TreeEntryFile(&e); err == nil {
				entry.Size = file.Size
			}
		}

		entries = append(entries, entry)
	}

	// Sort: directories first, then submodules, then files alphabetically
	sort.Slice(entries, func(i, j int) bool {
		// Directories first
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		// Then submodules
		if entries[i].IsSubmodule != entries[j].IsSubmodule {
			return entries[i].IsSubmodule
		}
		// Then alphabetically
		return entries[i].Name < entries[j].Name
	})

	return entries, nil
}

// GetBlob returns the content of a file in a repository
func GetBlob(reposPath, repoName, ref, path string) ([]byte, error) {
	repoPath := filepath.Join(reposPath, repoName+".git")
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}

	// Resolve the reference
	hash, err := resolveRef(r, ref)
	if err != nil {
		return nil, err
	}

	commit, err := r.CommitObject(hash)
	if err != nil {
		return nil, err
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}

	path = strings.Trim(path, "/")
	file, err := tree.File(path)
	if err != nil {
		return nil, err
	}

	reader, err := file.Reader()
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	return io.ReadAll(reader)
}

// GetCommits returns the commit history for a repository
func GetCommits(reposPath, repoName, ref string, limit int) ([]Commit, error) {
	repoPath := filepath.Join(reposPath, repoName+".git")
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}

	// Resolve the reference
	hash, err := resolveRef(r, ref)
	if err != nil {
		return nil, err
	}

	// Get commit iterator
	iter, err := r.Log(&git.LogOptions{
		From:  hash,
		Order: git.LogOrderCommitterTime,
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var commits []Commit
	count := 0
	err = iter.ForEach(func(c *object.Commit) error {
		if limit > 0 && count >= limit {
			return io.EOF
		}

		commits = append(commits, Commit{
			Hash:      c.Hash.String(),
			ShortHash: c.Hash.String()[:8],
			Message:   strings.TrimSpace(c.Message),
			Author:    c.Author.Name,
			Email:     c.Author.Email,
			Date:      c.Author.When,
		})
		count++
		return nil
	})

	if err != nil && err != io.EOF {
		return nil, err
	}

	return commits, nil
}

// GetBranches returns the list of branches for a repository
func GetBranches(reposPath, repoName string) ([]string, error) {
	repoPath := filepath.Join(reposPath, repoName+".git")
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}

	iter, err := r.Branches()
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var branches []string
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		branches = append(branches, ref.Name().Short())
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(branches)
	return branches, nil
}

// resolveRef resolves a branch name, tag, or commit hash to a commit hash
func resolveRef(r *git.Repository, ref string) (plumbing.Hash, error) {
	// First try as a branch
	branchRef, err := r.Reference(plumbing.NewBranchReferenceName(ref), true)
	if err == nil {
		return branchRef.Hash(), nil
	}

	// Try as a tag
	tagRef, err := r.Reference(plumbing.NewTagReferenceName(ref), true)
	if err == nil {
		return tagRef.Hash(), nil
	}

	// Try as HEAD
	if ref == "HEAD" || ref == "" {
		head, err := r.Head()
		if err == nil {
			return head.Hash(), nil
		}
	}

	// Try as a commit hash
	hash := plumbing.NewHash(ref)
	if _, err := r.CommitObject(hash); err == nil {
		return hash, nil
	}

	// Default to HEAD
	head, err := r.Head()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return head.Hash(), nil
}

// RepoExists checks if a repository exists
func RepoExists(reposPath, repoName string) bool {
	repoPath := filepath.Join(reposPath, repoName+".git")
	_, err := os.Stat(repoPath)
	return err == nil
}

// IsEmptyRepo checks if a repository has no commits
func IsEmptyRepo(repoPath string) bool {
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return true
	}

	// First try HEAD
	_, err = r.Head()
	if err == nil {
		return false
	}

	// HEAD might point to non-existent branch, check if any branches exist
	refs, err := r.References()
	if err != nil {
		return true
	}

	hasCommits := false
	refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Name().IsBranch() {
			hasCommits = true
			return io.EOF // Stop iteration
		}
		return nil
	})

	return !hasCommits
}

// CreateBareRepo creates a new bare git repository
func CreateBareRepo(repoPath string) error {
	_, err := git.PlainInit(repoPath, true)
	return err
}

// GetCommitDetails returns detailed information about a specific commit
func GetCommitDetails(reposPath, repoName, hash string) (*Commit, error) {
	repoPath := filepath.Join(reposPath, repoName+".git")
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}

	commitHash := plumbing.NewHash(hash)
	commit, err := r.CommitObject(commitHash)
	if err != nil {
		return nil, err
	}

	return &Commit{
		Hash:      commit.Hash.String(),
		ShortHash: commit.Hash.String()[:8],
		Message:   strings.TrimSpace(commit.Message),
		Author:    commit.Author.Name,
		Email:     commit.Author.Email,
		Date:      commit.Author.When,
	}, nil
}

// GetCommitDiff returns the diff for a specific commit
func GetCommitDiff(reposPath, repoName, hash string) (*CommitDiff, error) {
	repoPath := filepath.Join(reposPath, repoName+".git")
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}

	commitHash := plumbing.NewHash(hash)
	commit, err := r.CommitObject(commitHash)
	if err != nil {
		return nil, err
	}

	result := &CommitDiff{
		Commit: Commit{
			Hash:      commit.Hash.String(),
			ShortHash: commit.Hash.String()[:8],
			Message:   strings.TrimSpace(commit.Message),
			Author:    commit.Author.Name,
			Email:     commit.Author.Email,
			Date:      commit.Author.When,
		},
	}

	// Get parent commit for diff
	var parentTree *object.Tree
	if commit.NumParents() > 0 {
		parent, err := commit.Parent(0)
		if err == nil {
			result.ParentHash = parent.Hash.String()[:8]
			parentTree, _ = parent.Tree()
		}
	}

	// Get current commit's tree
	currentTree, err := commit.Tree()
	if err != nil {
		return result, nil // Return commit info without diff
	}

	// Calculate diff between parent and current
	var changes object.Changes
	if parentTree != nil {
		changes, err = parentTree.Diff(currentTree)
	} else {
		// First commit - show all files as added
		changes, err = object.DiffTree(nil, currentTree)
	}
	if err != nil {
		return result, nil
	}

	// Process each changed file
	for _, change := range changes {
		fileDiff := FileDiff{}

		// Determine file status and names
		action, err := change.Action()
		if err != nil {
			continue
		}

		switch action {
		case 1: // Insert
			fileDiff.Status = "added"
			fileDiff.Name = change.To.Name
		case 2: // Delete
			fileDiff.Status = "deleted"
			fileDiff.Name = change.From.Name
		case 3: // Modify
			if change.From.Name != change.To.Name {
				fileDiff.Status = "renamed"
				fileDiff.OldName = change.From.Name
				fileDiff.Name = change.To.Name
			} else {
				fileDiff.Status = "modified"
				fileDiff.Name = change.To.Name
			}
		}

		// Get file patch for diff lines
		patch, err := change.Patch()
		if err == nil && patch != nil {
			for _, fp := range patch.FilePatches() {
				if fp.IsBinary() {
					fileDiff.IsBinary = true
					continue
				}

				for _, chunk := range fp.Chunks() {
					diffChunk := DiffChunk{}
					lines := strings.Split(chunk.Content(), "\n")

					for _, line := range lines {
						if line == "" {
							continue
						}
						diffLine := DiffLine{Content: line}

						switch chunk.Type() {
						case 0: // Equal
							diffLine.Type = "context"
						case 1: // Add
							diffLine.Type = "add"
							fileDiff.Additions++
							result.Stats.Additions++
						case 2: // Delete
							diffLine.Type = "delete"
							fileDiff.Deletions++
							result.Stats.Deletions++
						}

						diffChunk.Lines = append(diffChunk.Lines, diffLine)
					}

					if len(diffChunk.Lines) > 0 {
						fileDiff.Chunks = append(fileDiff.Chunks, diffChunk)
					}
				}
			}
		}

		result.Files = append(result.Files, fileDiff)
		result.Stats.FilesChanged++
	}

	return result, nil
}

// ParseGitmodules reads and parses .gitmodules from a tree at given ref
func ParseGitmodules(reposPath, repoName, ref string) (map[string]*config.Submodule, error) {
	content, err := GetBlob(reposPath, repoName, ref, ".gitmodules")
	if err != nil {
		return nil, err // .gitmodules doesn't exist
	}

	modules := config.NewModules()
	if err := modules.Unmarshal(content); err != nil {
		return nil, err
	}

	// Return map keyed by path for easy lookup
	result := make(map[string]*config.Submodule)
	for _, sub := range modules.Submodules {
		result[sub.Path] = sub
	}
	return result, nil
}

// ConvertToWebURL converts a git URL to a browsable web URL
func ConvertToWebURL(gitURL, hash string) string {
	// Handle SSH format: git@host:user/repo.git
	sshRegex := regexp.MustCompile(`^git@([^:]+):(.+?)(?:\.git)?$`)
	if matches := sshRegex.FindStringSubmatch(gitURL); matches != nil {
		host := matches[1]
		path := strings.TrimSuffix(matches[2], ".git")
		return formatWebURL(host, path, hash)
	}

	// Handle HTTPS format: https://host/user/repo.git
	if parsed, err := url.Parse(gitURL); err == nil && parsed.Host != "" {
		path := strings.TrimSuffix(parsed.Path, ".git")
		path = strings.TrimPrefix(path, "/")
		return formatWebURL(parsed.Host, path, hash)
	}

	return gitURL // Return original if can't parse
}

func formatWebURL(host, path, hash string) string {
	switch {
	case strings.Contains(host, "github.com"):
		return fmt.Sprintf("https://github.com/%s/tree/%s", path, hash)
	case strings.Contains(host, "gitlab.com"):
		return fmt.Sprintf("https://gitlab.com/%s/-/tree/%s", path, hash)
	case strings.Contains(host, "bitbucket.org"):
		return fmt.Sprintf("https://bitbucket.org/%s/src/%s", path, hash)
	case strings.Contains(host, "git.rafayel.dev"):
		// Internal gitraf - extract repo name and link locally
		parts := strings.Split(path, "/")
		if len(parts) > 0 {
			repoName := strings.TrimSuffix(parts[len(parts)-1], ".git")
			return fmt.Sprintf("/%s/tree/%s/", repoName, hash)
		}
		fallthrough
	default:
		return fmt.Sprintf("https://%s/%s", host, path)
	}
}

// GetSubmoduleInfo returns detailed information about a specific submodule
func GetSubmoduleInfo(reposPath, repoName, ref, submodulePath string) (*SubmoduleInfo, error) {
	repoPath := filepath.Join(reposPath, repoName+".git")
	r, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}

	hash, err := resolveRef(r, ref)
	if err != nil {
		return nil, err
	}

	commit, err := r.CommitObject(hash)
	if err != nil {
		return nil, err
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}

	// Find the submodule entry in the tree
	submodulePath = strings.Trim(submodulePath, "/")
	entry, err := tree.FindEntry(submodulePath)
	if err != nil {
		return nil, fmt.Errorf("submodule not found: %s", submodulePath)
	}

	if entry.Mode != filemode.Submodule {
		return nil, fmt.Errorf("path is not a submodule: %s", submodulePath)
	}

	info := &SubmoduleInfo{
		Path:      submodulePath,
		Name:      filepath.Base(submodulePath),
		Hash:      entry.Hash.String(),
		ShortHash: entry.Hash.String()[:8],
		Status:    "configured",
	}

	// Try to get .gitmodules config
	if submodules, err := ParseGitmodules(reposPath, repoName, ref); err == nil {
		if sub, exists := submodules[submodulePath]; exists {
			info.Name = sub.Name
			info.URL = sub.URL
			info.Branch = sub.Branch
			info.WebURL = ConvertToWebURL(sub.URL, info.Hash)
		} else {
			info.Status = "missing-config"
		}
	} else {
		info.Status = "missing-config"
	}

	return info, nil
}

// GetSubmodulesForPath returns submodule info for entries at a given path
func GetSubmodulesForPath(reposPath, repoName, ref, path string) (map[string]*SubmoduleInfo, error) {
	entries, err := GetTree(reposPath, repoName, ref, path)
	if err != nil {
		return nil, err
	}

	// Parse .gitmodules once
	gitmodules, _ := ParseGitmodules(reposPath, repoName, ref)

	result := make(map[string]*SubmoduleInfo)
	for _, entry := range entries {
		if !entry.IsSubmodule {
			continue
		}

		fullPath := entry.Name
		if path != "" && path != "/" {
			fullPath = filepath.Join(strings.Trim(path, "/"), entry.Name)
		}

		info := &SubmoduleInfo{
			Path:      fullPath,
			Name:      entry.Name,
			Hash:      entry.Hash,
			ShortHash: entry.Hash[:8],
			Status:    "configured",
		}

		if gitmodules != nil {
			if sub, exists := gitmodules[fullPath]; exists {
				info.Name = sub.Name
				info.URL = sub.URL
				info.Branch = sub.Branch
				info.WebURL = ConvertToWebURL(sub.URL, info.Hash)
			} else {
				info.Status = "missing-config"
			}
		} else {
			info.Status = "missing-config"
		}

		result[entry.Name] = info
	}

	return result, nil
}
