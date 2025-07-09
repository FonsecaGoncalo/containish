#!/usr/bin/env bash
set -euo pipefail

# Ensure we have the minimal alpine rootfs
if [ ! -d "./alpine" ]; then
    echo "./alpine root filesystem not found." >&2
    echo "Please create or download an Alpine rootfs at ./alpine before running." >&2
    exit 1
fi

# Check for vagrant
if ! command -v vagrant >/dev/null 2>&1; then
    echo "Vagrant is not installed. Please install Vagrant." >&2
    exit 1
fi

# Ensure the VM is running
if ! vagrant status --machine-readable | grep -q ',state,running'; then
    echo "Starting Vagrant VM..."
    vagrant up --provision
fi

# Run the Go integration tests inside the VM
echo "Running Go integration tests inside VM..."
vagrant ssh -c "cd /vagrant && IN_VM=1 go test ./integration -v"

echo "Integration tests completed"
