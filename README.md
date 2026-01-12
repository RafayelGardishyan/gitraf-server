# gitraf-server

A minimal Go web server for browsing git repositories with Tailscale tailnet-aware access control.

## Features

- **Tailnet-aware access control** - Shows all repos when accessed from tailnet, only public repos otherwise
- **Repository browser** - Browse files, view contents, commit history
- **Submodule support** - Full display with commit hash, URL, status, and external links
- **GitHub mirroring** - Configure mirrors via web UI with SSH key management
- **Repository settings** - Configure visibility, pages, and mirroring from the web
- **One-click updates** - Update server to latest version from the settings page
- **Minimal UI** - Clean, responsive design with dark/light mode support
- **Public repo detection** - Uses `git-daemon-export-ok` file to determine visibility

## Installation

### From Source

```bash
go build -o gitraf-server .
```

### Docker

```bash
docker build -t gitraf-server .
docker run -v /path/to/repos:/data/repos:ro \
  -e GITRAF_REPOS_PATH=/data/repos \
  -p 8080:8080 \
  gitraf-server
```

## Usage

```bash
gitraf-server \
  --repos /opt/ogit/data/repos \
  --port 8080 \
  --public-url https://git.example.com \
  --tailnet-url myserver.tail12345.ts.net
```

### Command Line Flags

| Flag | Description | Default |
|------|-------------|---------|
| `--repos` | Path to git repositories directory | Required |
| `--port` | Port to listen on | 8080 |
| `--public-url` | Public HTTPS URL for clone instructions | |
| `--tailnet-url` | Tailnet URL for SSH clone instructions | |
| `--templates` | Path to templates directory | ./templates |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `GITRAF_REPOS_PATH` | Path to git repositories directory |
| `GITRAF_PORT` | Port to listen on |
| `GITRAF_PUBLIC_URL` | Public HTTPS URL |
| `GITRAF_TAILNET_URL` | Tailnet URL |

## Access Model

| Location | Private Repos | Public Repos |
|----------|---------------|--------------|
| Tailnet (100.64.0.0/10) | Visible | Visible |
| External | Not visible | Visible |

A repository is considered public if it contains a `git-daemon-export-ok` file.

## Docker Compose

```yaml
gitraf-web:
  build: ./gitraf-server
  container_name: gitraf-web
  restart: unless-stopped
  ports:
    - "127.0.0.1:8081:8080"
  volumes:
    - ./data/repos:/data/repos:ro
  environment:
    - GITRAF_REPOS_PATH=/data/repos
    - GITRAF_PUBLIC_URL=https://git.example.com
    - GITRAF_TAILNET_URL=myserver.tail12345.ts.net
```

## nginx Configuration

```nginx
# Proxy web UI to gitraf-server
location / {
    proxy_pass http://127.0.0.1:8081;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
}
```

## Web Interface Features

### Settings Architecture

gitraf separates settings into two categories:

1. **Repository Settings** (`/{repo}/settings`) - Per-repository configuration
2. **Server Admin Settings** (`/admin/settings`) - Server-wide configuration (Tailnet only)

#### Repository Settings

Access at `/{repo}/settings` (Tailnet required for changes):

- **General**: Description and visibility (public/private)
- **Pages**: Enable/disable static site hosting, configure branch, build command, and output directory
- **GitHub Mirror**: Configure automatic syncing to GitHub with SSH key management

#### Server Admin Settings

Access at `/admin/settings` (Tailnet only):

- **LFS Storage**: Configure S3-compatible storage for Git LFS objects
- **S3 Backup**: Configure automated R2/S3 backup with schedule settings
- **SSH Key Management**: Generate and view SSH keys for GitHub mirroring
- **Server Update**: One-click update to latest version

### Submodule Display

Repositories with submodules show:
- Purple folder icon to distinguish from regular directories
- Commit hash reference
- Link to external repository (GitHub, GitLab, etc.)
- Detailed view with URL, branch, and status

### Configuration Files

gitraf-server stores configuration in the parent directory of the repos path:

| File | Description |
|------|-------------|
| `lfs-config.json` | LFS S3 storage configuration |
| `backup-config.json` | R2/S3 backup configuration |
| `ssh/id_ed25519` | SSH private key for GitHub mirroring |
| `ssh/id_ed25519.pub` | SSH public key |

#### LFS Config Schema (`lfs-config.json`)

```json
{
  "enabled": true,
  "endpoint": "https://s3.us-west-2.amazonaws.com",
  "bucket": "my-lfs-bucket",
  "region": "us-west-2",
  "access_key": "AKIA...",
  "secret_key": "..."
}
```

#### Backup Config Schema (`backup-config.json`)

```json
{
  "enabled": true,
  "endpoint": "https://xxx.r2.cloudflarestorage.com",
  "bucket": "gitraf-backup",
  "access_key": "...",
  "secret_key": "...",
  "schedule": "0 3 * * *"
}
```

## License

MIT
