# mc-fuse

Run a Minecraft server from config files that keep `${VAR}` placeholders on disk.

`mc-fuse` mounts your server directory through FUSE, decrypts SOPS secrets in memory, and swaps placeholders to real values only when the server reads them. If Paper or a plugin writes config files back, `mc-fuse` writes placeholders again instead of plaintext secrets.

## Why this exists

Typical secret handling for Minecraft servers looks like this:

- decrypt secrets
- render config files
- start the server
- hope nothing writes plaintext back to disk

`mc-fuse` changes that flow:

- source files keep `${VAR}` markers
- secrets are decrypted only in memory
- reads return real values
- writes go back to `${VAR}`

This is useful when you keep server files in Git, backups, sync jobs, or shared infra where plaintext secrets on disk are a problem.

## How it works

```text
encrypted secrets.yaml
        |
        | sops --decrypt
        v
    in-memory map
        |
        | mc-fuse mount
        v
 source dir -----> mounted dir

 read  : ${DB_PASSWORD} -> super-secret
 write : super-secret   -> ${DB_PASSWORD}
```

## Features

- zero plaintext secrets written by `mc-fuse`
- supports shared values plus per-server overrides
- validates placeholders before launch
- auto-detects the server JAR
- optional auto-restart on crash
- simple single-binary CLI

## Requirements

- Linux
- FUSE available at `/dev/fuse`
- `sops` installed and configured
- Java installed
- Go 1.22+ only if you build from source

## Install

### Download a release

Use the binaries from GitHub Releases for:

- `linux-amd64`
- `linux-arm64`

### Build locally

```bash
go build -o mc-fuse .
```

### Cross-compile

```bash
GOOS=linux GOARCH=arm64 go build -o mc-fuse-linux-arm64 .
GOOS=linux GOARCH=amd64 go build -o mc-fuse-linux-amd64 .
```

## Quick start

### 1. Put placeholders in config files

```properties
server-port=${SERVER_PORT}
rcon.password=${RCON_PASSWORD}
motd=${SERVER_MOTD}
```

```yaml
data:
  address: ${DB_HOST}
  username: ${DB_USER}
  password: ${DB_PASSWORD}
```

### 2. Create secrets

```yaml
SERVER_PORT: "25565"
RCON_PASSWORD: "change-me"
SERVER_MOTD: "My server"
DB_HOST: "127.0.0.1:5432"
DB_USER: "minecraft"
DB_PASSWORD: "super-secret"
```

### 3. Encrypt them with SOPS

```bash
age-keygen -o ~/.config/sops/age/keys.txt
AGE_PUB=$(age-keygen -y ~/.config/sops/age/keys.txt)

sops --encrypt --age "$AGE_PUB" secrets.yaml > secrets.enc.yaml
rm secrets.yaml
```

### 4. Validate before launch

```bash
./mc-fuse --secrets secrets.enc.yaml --dry-run ./server
```

### 5. Start the server

```bash
./mc-fuse --secrets secrets.enc.yaml ./server
```

## Usage

```text
mc-fuse --secrets <file> [options] <server-directory>
```

## Flags

| Flag | Default | What it does |
|------|---------|--------------|
| `--secrets` | required | SOPS-encrypted server secrets |
| `--values` | | Shared SOPS-encrypted values loaded first |
| `--ram` | `4G` | Java `-Xmx` |
| `--min-ram` | `512M` | Java `-Xms` |
| `--mount` | `deployments/<name>` | Mount directory |
| `--jar` | auto | Server jar path inside the source directory |
| `--java-opts` | | Extra JVM flags |
| `--dry-run` | `false` | Validate placeholders and exit |
| `--restart` | `false` | Restart after a crash |
| `--debug` | `false` | Enable FUSE debug logs |
| `--verbose` | `false` | Log key FUSE operations |
| `--version` | `false` | Print version |

## Examples

### Basic

```bash
./mc-fuse --secrets secrets.enc.yaml ./servers/lobby
```

### Shared values + server overrides

```bash
./mc-fuse \
  --values values.enc.yaml \
  --secrets servers/lobby/secrets.enc.yaml \
  ./servers/lobby
```

### Custom RAM and restart

```bash
./mc-fuse --secrets secrets.enc.yaml --ram 8G --restart ./servers/survival
```

### Custom mount path

```bash
./mc-fuse --secrets secrets.enc.yaml --mount /tmp/mc-lobby ./servers/lobby
```

## Shared values

Use `--values` for secrets reused across servers.

`values.enc.yaml` is loaded first.
`--secrets` is loaded after that and overrides duplicate keys.

Example:

```yaml
DB_HOST: "localhost:5432"
DB_USER: "mc-postgres"
DB_PASSWORD: "shared-password"
VELOCITY_SECRET: "change-me"
```

```yaml
SERVER_PORT: "25665"
SERVER_MOTD: "Lobby"
DB_PASSWORD: "override-for-this-server"
```

## Supported file types

These files get placeholder substitution:

- `.yml`
- `.yaml`
- `.properties`
- `.toml`
- `.conf`
- `.json`
- `.txt`
- `.cfg`
- `.ini`
- `.secret`

Other files pass through unchanged.

## Safety model

What `mc-fuse` does:

- keeps placeholders in the source directory
- decrypts secrets only when starting
- keeps decrypted values in process memory
- writes placeholders back on config saves

What `mc-fuse` does not do:

- protect secrets from root
- protect secrets already present in JVM memory
- replace full host hardening

Recommended:

- run under a dedicated user
- disable core dumps
- restrict who can inspect processes
- keep `allow_other` disabled

## Example project

See [example/README.md](example/README.md) for a complete working example.

## Releases

This repository includes a GitHub Actions workflow that:

- builds on push and pull request
- cross-compiles `linux-amd64` and `linux-arm64`
- publishes `.tar.gz` archives on version tags like `v1.2.0`

## Development

```bash
go build ./...
go test ./...
```

## License

MIT
