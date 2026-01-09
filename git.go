package main

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
	Name    string
	IsDir   bool
	Mode    string
	Size    int64
	Hash    string
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
			Name:  e.Name,
			IsDir: e.Mode.IsFile() == false,
			Mode:  e.Mode.String(),
			Hash:  e.Hash.String(),
		}

		// Get file size if it's a file
		if e.Mode.IsFile() {
			if file, err := tree.TreeEntryFile(&e); err == nil {
				entry.Size = file.Size
			}
		}

		entries = append(entries, entry)
	}

	// Sort: directories first, then alphabetically
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
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
