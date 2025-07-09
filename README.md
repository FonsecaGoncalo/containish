<div align="center">

<picture>
  <source media="(prefers-color-scheme: light)" srcset="/img_1.png">
  <img alt="containish logo" src="/img_1.png" width="25%" height="25%">
</picture>

Containish: A simplistic containerization system written in Go
</div>

---

## 📋 Requirements

- **Go** (1.16 or higher) 🏁
- **Linux** 🐧
- **Root Access** 🔑

---

## 🛠️ Installation

```bash
git clone https://github.com/yourusername/containish.git
cd containish
go build -o containish
```

---

## 🧪 Running Integration Tests

The `scripts/integration_test.sh` helper boots the Vagrant VM and executes the
Go integration tests inside it. Ensure Vagrant is installed and that an Alpine
root filesystem exists at `./alpine` before running the script. A minimal
`config.json` using the [OCI runtime-spec](https://github.com/opencontainers/runtime-spec)
is provided at the repository root and is used by default when running
`containish run`.

```bash
./scripts/integration_test.sh
```

The tests build the binary and launch a container that should print `hello`.
If the output ends with the usual Go test `PASS` line, the container behaved as
expected.

## 🚀 Running Containers

Use `containish run <id>` to start a container using the default `config.json`.
Add the `-d` flag to detach and return immediately after the container starts:

```bash
sudo ./containish run -d mycontainer
```

Stop a running container with:

```bash
sudo ./containish stop mycontainer
```
