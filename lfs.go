package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/go-chi/chi/v5"
)

// LFSConfig holds S3 configuration for LFS storage
type LFSConfig struct {
	Endpoint  string `json:"endpoint"`
	Bucket    string `json:"bucket"`
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
	Region    string `json:"region"`
}

// LFSBatchRequest is the Git LFS batch API request
type LFSBatchRequest struct {
	Operation string       `json:"operation"`
	Transfers []string     `json:"transfers,omitempty"`
	Ref       *LFSRef      `json:"ref,omitempty"`
	Objects   []LFSObject  `json:"objects"`
}

// LFSRef represents a Git reference
type LFSRef struct {
	Name string `json:"name"`
}

// LFSObject represents an LFS object
type LFSObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// LFSBatchResponse is the Git LFS batch API response
type LFSBatchResponse struct {
	Transfer string              `json:"transfer,omitempty"`
	Objects  []LFSObjectResponse `json:"objects"`
}

// LFSObjectResponse is the response for a single object
type LFSObjectResponse struct {
	OID           string                 `json:"oid"`
	Size          int64                  `json:"size"`
	Authenticated bool                   `json:"authenticated,omitempty"`
	Actions       map[string]*LFSAction  `json:"actions,omitempty"`
	Error         *LFSError              `json:"error,omitempty"`
}

// LFSAction describes how to upload/download an object
type LFSAction struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header,omitempty"`
	ExpiresIn int               `json:"expires_in,omitempty"`
	ExpiresAt string            `json:"expires_at,omitempty"`
}

// LFSError represents an error for a specific object
type LFSError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// loadLFSConfig loads the LFS configuration from file
func loadLFSConfig(configPath string) (*LFSConfig, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	var cfg LFSConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// createS3Client creates an S3 client from LFS config
func createS3Client(ctx context.Context, lfsCfg *LFSConfig) (*s3.Client, error) {
	region := lfsCfg.Region
	if region == "" || region == "auto" {
		region = "auto"
	}

	// Create custom endpoint resolver for S3-compatible services
	customResolver := aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
		return aws.Endpoint{
			URL:               lfsCfg.Endpoint,
			SigningRegion:     region,
			HostnameImmutable: true,
		}, nil
	})

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion(region),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			lfsCfg.AccessKey,
			lfsCfg.SecretKey,
			"",
		)),
		config.WithEndpointResolverWithOptions(customResolver),
	)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true // Required for some S3-compatible services
	})

	return client, nil
}

// handleLFSBatch handles the LFS batch API endpoint
func (s *Server) handleLFSBatch(w http.ResponseWriter, r *http.Request) {
	repoName := chi.URLParam(r, "repo")

	// Check if repo exists and is accessible
	if !RepoExists(s.reposPath, repoName) {
		http.Error(w, "Repository not found", http.StatusNotFound)
		return
	}

	// Check access (only allow from tailnet for write operations)
	repoPath := filepath.Join(s.reposPath, repoName+".git")
	isPublic := IsPublicRepo(repoPath)
	isTailnet := s.isTailnetRequest(r)

	// Parse request
	var req LFSBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// For upload operations, require tailnet access
	if req.Operation == "upload" && !isTailnet {
		http.Error(w, "Upload requires tailnet access", http.StatusForbidden)
		return
	}

	// For download operations on private repos, require tailnet
	if req.Operation == "download" && !isPublic && !isTailnet {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Load LFS config
	lfsConfigPath := filepath.Join(filepath.Dir(s.reposPath), "lfs-config.json")
	lfsCfg, err := loadLFSConfig(lfsConfigPath)
	if err != nil {
		log.Printf("LFS config error: %v", err)
		http.Error(w, "LFS not configured", http.StatusServiceUnavailable)
		return
	}

	// Create S3 client
	ctx := r.Context()
	s3Client, err := createS3Client(ctx, lfsCfg)
	if err != nil {
		log.Printf("S3 client error: %v", err)
		http.Error(w, "Storage error", http.StatusInternalServerError)
		return
	}

	// Process objects
	presignClient := s3.NewPresignClient(s3Client)
	response := LFSBatchResponse{
		Transfer: "basic",
		Objects:  make([]LFSObjectResponse, len(req.Objects)),
	}

	for i, obj := range req.Objects {
		response.Objects[i] = LFSObjectResponse{
			OID:  obj.OID,
			Size: obj.Size,
		}

		// S3 key format: {repo}/{oid[0:2]}/{oid[2:4]}/{oid}
		s3Key := fmt.Sprintf("%s/%s/%s/%s", repoName, obj.OID[:2], obj.OID[2:4], obj.OID)

		if req.Operation == "upload" {
			// Generate presigned PUT URL
			presignReq, err := presignClient.PresignPutObject(ctx, &s3.PutObjectInput{
				Bucket:        aws.String(lfsCfg.Bucket),
				Key:           aws.String(s3Key),
				ContentLength: aws.Int64(obj.Size),
			}, func(opts *s3.PresignOptions) {
				opts.Expires = time.Hour
			})
			if err != nil {
				response.Objects[i].Error = &LFSError{
					Code:    500,
					Message: "Failed to generate upload URL",
				}
				continue
			}

			response.Objects[i].Actions = map[string]*LFSAction{
				"upload": {
					Href:      presignReq.URL,
					ExpiresIn: 3600,
				},
			}
		} else if req.Operation == "download" {
			// Check if object exists
			_, err := s3Client.HeadObject(ctx, &s3.HeadObjectInput{
				Bucket: aws.String(lfsCfg.Bucket),
				Key:    aws.String(s3Key),
			})
			if err != nil {
				response.Objects[i].Error = &LFSError{
					Code:    404,
					Message: "Object not found",
				}
				continue
			}

			// Generate presigned GET URL
			presignReq, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
				Bucket: aws.String(lfsCfg.Bucket),
				Key:    aws.String(s3Key),
			}, func(opts *s3.PresignOptions) {
				opts.Expires = time.Hour
			})
			if err != nil {
				response.Objects[i].Error = &LFSError{
					Code:    500,
					Message: "Failed to generate download URL",
				}
				continue
			}

			response.Objects[i].Actions = map[string]*LFSAction{
				"download": {
					Href:      presignReq.URL,
					ExpiresIn: 3600,
				},
			}
		}
	}

	// Send response
	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	json.NewEncoder(w).Encode(response)
}

// handleLFSLocksVerify handles the LFS locks verify endpoint (stub)
func (s *Server) handleLFSLocksVerify(w http.ResponseWriter, r *http.Request) {
	// LFS file locking is not implemented - return empty response
	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ours":   []interface{}{},
		"theirs": []interface{}{},
	})
}

// handleLFSLocks handles the LFS locks list endpoint (stub)
func (s *Server) handleLFSLocks(w http.ResponseWriter, r *http.Request) {
	// LFS file locking is not implemented - return empty response
	w.Header().Set("Content-Type", "application/vnd.git-lfs+json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"locks": []interface{}{},
	})
}

// LFSMiddleware adds required headers for LFS requests
func LFSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is an LFS request
		if strings.Contains(r.URL.Path, "/info/lfs/") {
			w.Header().Set("Accept", "application/vnd.git-lfs+json")
		}
		next.ServeHTTP(w, r)
	})
}
