# gitraf-server

A minimal Go web server for browsing git repositories with Tailscale tailnet-aware access control.

## Features

- **Tailnet-aware access control** - Shows all repos when accessed from tailnet, only public repos otherwise
- **Repository browser** - Browse files, view contents, commit history
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

## License

MIT
