# mc-fuse example

A complete working example showing how to launch a Minecraft Paper server with mc-fuse secret injection.

## Directory structure

```
example/
├── README.md              ← you are here
├── values.yaml            ← global shared secrets (encrypt with SOPS)
├── secrets.yaml           ← server-specific secrets (encrypt with SOPS)
└── server/                ← Minecraft server directory
    ├── eula.txt
    ├── server.properties  ← contains ${VAR} placeholders
    ├── config/
    │   └── paper-global.yml
    └── plugins/
        └── LuckPerms/
            └── config.yml
```

## Setup

### 1. Install prerequisites

```bash
# FUSE
sudo apt install -y libfuse3-3 fuse3

# age (encryption)
sudo apt install -y age
# or: go install filippo.io/age/cmd/...@latest

# SOPS
# Download from https://github.com/getsops/sops/releases
wget https://github.com/getsops/sops/releases/download/v3.9.4/sops-v3.9.4.linux.arm64
chmod +x sops-v3.9.4.linux.arm64
sudo mv sops-v3.9.4.linux.arm64 /usr/local/bin/sops

# Java 21
sudo apt install -y java-21-amazon-corretto-jdk
# or any Java 21 distribution
```

### 2. Generate encryption key

```bash
mkdir -p ~/.config/sops/age
age-keygen -o ~/.config/sops/age/keys.txt
AGE_PUB=$(age-keygen -y ~/.config/sops/age/keys.txt)
echo "Your public key: $AGE_PUB"
```

### 3. Edit secrets (fill in your values)

```bash
# Edit values.yaml and secrets.yaml with your actual values, then encrypt:
sops --encrypt --age "$AGE_PUB" values.yaml > values.enc.yaml
sops --encrypt --age "$AGE_PUB" secrets.yaml > secrets.enc.yaml

# Delete plaintext
rm values.yaml secrets.yaml
```

### 4. Build mc-fuse

```bash
# From the repo root:
go build -o mc-fuse .
# or via Docker:
docker run --rm -v "$PWD/..":/app -w /app golang:1.24-bookworm sh -c "go build -o mc-fuse ."
```

### 5. Dry-run (validate)

```bash
# Check all placeholders resolve before starting:
../mc-fuse --values values.enc.yaml --secrets secrets.enc.yaml --dry-run server/
```

Expected output:
```
[mc-fuse] Decrypting global values: /path/to/values.enc.yaml
[mc-fuse] Loaded 4 global values
[mc-fuse] Decrypting secrets: /path/to/secrets.enc.yaml
[mc-fuse] Loaded 6 secrets total (4 global + 2 server-specific)
[mc-fuse] Validating placeholders in: /path/to/server
[mc-fuse] Validation OK — all placeholders have matching secrets.
[mc-fuse] --dry-run: validation complete, server will not start.
```

### 6. Launch

```bash
../mc-fuse --values values.enc.yaml --secrets secrets.enc.yaml --ram 4G server/
```

The server will start with all `${VAR}` placeholders resolved to real values. Config files on disk still contain `${VAR}` — secrets exist only in the FUSE layer.