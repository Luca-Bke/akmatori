# SSH Tool Command Validation

The SSH tool implements a multi-layered command validation system to prevent destructive operations while allowing necessary diagnostic and monitoring commands.

## Overview

The `CommandValidator` enforces three security modes:

| Mode | Description | Dangerous Patterns | Write Redirects |
|------|-------------|-------------------|-----------------|
| **Read-only** (default) | Only whitelisted read-only commands | ✅ Blocked | ✅ Blocked |
| **Custom Allowlist** | Operator-defined command list | ✅ Blocked | ✅ Blocked |
| **Write-enabled** | All commands permitted | ❌ Allowed | ❌ Allowed |

---

## Read-Only Mode (Default)

When `allow_write_commands` is `false` (default), only these base commands are permitted:

### File Viewing
```
cat, head, tail, less, more
```

### Search and Find
```
grep, rg (ripgrep), fzf, find, locate, which, type
```

### Directory Listing
```
ls, pwd, tree
```

### System Information
```
whoami, uname, hostname, date, id
uptime, w, who, last
nproc, lscpu, getconf
```

### Process Information
```
ps, top, htop, pgrep, pstree
```

### Performance Monitoring
```
mpstat, sar, iostat, vmstat
nmon, iotop, pidstat
```

### Conditional Tests
```
test, [
```

### Memory and Disk Information
```
df, du, free, lsblk
```

### Network Information
```
netstat, ss, ip, ifconfig, ping
traceroute, dig, nslookup, host
```

### Environment
```
env, printenv, echo
```

### Text Processing (Read-Only Operations)
```
wc, sort, uniq, cut, awk
sed, tr, diff, comm
```

### File Information
```
stat, file, md5sum, sha256sum
```

### Logs
```
journalctl, dmesg
```

### Commands with Subcommand Restrictions
These commands are allowed but restricted to specific subcommands (see [Subcommand Restrictions](#subcommand-restrictions)):
```
docker, kubectl, systemctl
dpkg, rpm, apt, yum
```

### Sudo
```
sudo
```
When `sudo` is used, the inner command is recursively validated against the same read-only rules.

---

## Subcommand Restrictions

Certain commands have restricted subcommands in read-only mode:

### Docker
| ✅ Allowed | ❌ Blocked |
|-----------|-----------|
| `docker ps` | `docker rm` |
| `docker images` | `docker rmi` |
| `docker logs` | `docker stop` |
| `docker inspect` | `docker kill` |
| `docker stats` | `docker exec` |
| `docker top` | `docker run` |
| `docker info` | `docker start` |
| `docker version` | |
| `docker network ls` | |
| `docker volume ls` | |

### Kubectl
| ✅ Allowed | ❌ Blocked |
|-----------|-----------|
| `kubectl get` | `kubectl delete` |
| `kubectl describe` | `kubectl apply` |
| `kubectl logs` | `kubectl create` |
| `kubectl top` | `kubectl exec` |
| `kubectl version` | `kubectl edit` |
| `kubectl config view` | `kubectl patch` |
| `kubectl cluster-info` | |

### Systemctl
| ✅ Allowed | ❌ Blocked |
|-----------|-----------|
| `systemctl status` | `systemctl start` |
| `systemctl is-active` | `systemctl stop` |
| `systemctl is-enabled` | `systemctl restart` |
| `systemctl list-units` | `systemctl enable` |
| `systemctl list-unit-files` | `systemctl disable` |
| `systemctl show` | |

### Package Managers
| Command | ✅ Allowed Subcommands |
|---------|----------------------|
| `apt` | `list`, `show`, `search`, `policy` |
| `yum` | `list`, `info`, `search` |
| `dpkg` | `-l`, `-L`, `-s`, `--list`, `--listfiles`, `--status` |
| `rpm` | `-qa`, `-qi`, `-ql`, `--query` |

---

## Dangerous Patterns (Always Blocked in Read-Only and Custom Modes)

These patterns are **always blocked** when read-only mode or custom allowlist is enabled:

### Destructive File Operations
```
rm, rmdir, shred
```

### File Modification
```
mv, cp, chmod, chown, chgrp
```

### Process Control
```
kill, killall, pkill
```

### System Control
```
shutdown, reboot, halt, poweroff, init
```

### Disk Operations
```
dd, mkfs, fdisk, parted, mount, umount
```

### User Management
```
useradd, userdel, usermod, passwd, groupadd
```

### Package Modification
```
apt install/remove/purge, apt-get install/remove
yum install/remove/erase
dnf install/remove
pip install/uninstall
npm install/uninstall
```

### Service Control
```
systemctl start/stop/restart/enable/disable
service start/stop/restart
```

### Network Modification
```
iptables, firewall-cmd, ufw
```

### Privilege Escalation
```
su
```

### Other Dangerous Commands
```
mkfifo, mknod
:(){ :|:& };:  (fork bomb)
```

### Output Redirects
```
>  (file output redirect, detected intelligently)
>> (append redirect)
```
Note: `2>` and `>&` (stderr redirects) are permitted.

---

## Custom Allowlist Mode

When `allowed_commands` is configured for a host, it **overrides** the default read-only list:

```json
{
  "hostname": "web-prod-1",
  "address": "10.0.1.5",
  "allowed_commands": ["curl", "wget", "cat", "ls", "grep", "journalctl"]
}
```

**Rules:**
- Only commands in the list are permitted
- Dangerous patterns are **still blocked**
- Write redirects are **still blocked**
- No subcommand restrictions (only base command is checked)
- `sudo` recursively validates the inner command against the custom list

---

## Write-Enabled Mode

When `allow_write_commands` is `true` for a host:

- All commands are permitted
- No validation is performed
- Dangerous patterns are **not** checked
- Write redirects are **allowed**

⚠️ **Use with caution** — this disables all command validation.

---

## Command Chain Validation

Commands joined by separators are validated individually:

```bash
# Each command in the chain is checked separately
ls -la && grep "error" /var/log/syslog | sort | uniq -c
```

Separators that trigger split-validation:
- `;` (sequential execution)
- `&&` (and)
- `||` (or)
- `|` (pipe)

---

## Configuration

### Per-Host Settings

Each host in `ssh_hosts` can have independent security settings:

```json
{
  "ssh_hosts": [
    {
      "hostname": "web-prod-1",
      "address": "10.0.1.5",
      "allow_write_commands": false,
      "allowed_commands": []
    },
    {
      "hostname": "db-prod-1",
      "address": "10.0.2.5",
      "allow_write_commands": true
    }
  ]
}
```

### Ad-Hoc Connections

When `allow_adhoc_connections` is enabled, ad-hoc connections use:

| Setting | Default | Description |
|---------|---------|-------------|
| `adhoc_default_user` | `root` | Default SSH user |
| `adhoc_default_port` | `22` | Default SSH port |
| `adhoc_allow_write_commands` | `false` | Read-only by default |

---

## Validation Flow

```
Command received
       │
       ▼
┌─────────────────────┐
│ Custom Allowlist?   │──Yes──► Check custom list + dangerous patterns
└─────────────────────┘         │
       │ No                     ▼
       ▼                  Validate against
┌─────────────────────┐   custom allowlist
│ Write Enabled?      │──Yes──► Allow all
└─────────────────────┘         │
       │ No                     ▼
       ▼                  Return success
┌─────────────────────┐
│ Check dangerous     │──Match──► Block with error
│ patterns            │
└─────────────────────┘
       │ No match
       ▼
┌─────────────────────┐
│ Check write         │──Found──► Block with error
│ redirects           │
└─────────────────────┘
       │ No redirect
       ▼
┌─────────────────────┐
│ Split on separators │
│ (; && || |)         │
└─────────────────────┘
       │
       ▼
┌─────────────────────┐
│ For each command:   │
│ ├─ Extract base cmd │
│ ├─ Check allowlist  │
│ ├─ Handle sudo      │
│ └─ Check subcmds    │
└─────────────────────┘
       │
       ▼
   Allow command
```

---

## Error Messages

### Read-Only Mode Violation
```
command blocked: 'rm -rf /tmp/*' contains dangerous pattern 'rm ' (read-only mode is enabled)

Allowed commands in read-only mode:
  File viewing: cat, head, tail, less, more
  Search: grep, rg, fzf, find, locate, which
  Directory: ls, pwd, tree
  System info: whoami, uname, hostname, date, id, uptime, nproc, lscpu, getconf
  Processes: ps, top, htop, pgrep, pstree
  Performance: mpstat, sar, iostat, vmstat, pidstat, nmon, iotop
  Resources: df, du, free, lsblk
  Network: netstat, ss, ip, ping, dig, traceroute
  Text processing: wc, sort, uniq, cut, awk, sed, tr
  File info: stat, file, md5sum, sha256sum
  Logs: journalctl, dmesg
  Containers: docker ps/images/logs/inspect/stats, kubectl get/describe/logs

To allow write commands, enable 'Allow Write Commands' for this host
```

### Custom Allowlist Violation
```
command blocked: 'curl' is not in the allowed command list (custom command allowlist is enabled)

Allowed commands: cat, ls, grep, journalctl

To add more commands, edit the 'Allowed Commands' list for this host in tool settings
```

---

## Implementation Details

- **Source:** `mcp-gateway/internal/tools/ssh/command_validator.go`
- **Tests:** `mcp-gateway/internal/tools/ssh/command_validator_test.go`
- **Integration:** `mcp-gateway/internal/tools/ssh/ssh.go`

### Key Functions

| Function | Purpose |
|----------|---------|
| `NewCommandValidator()` | Creates validator with default read-only list |
| `NewCommandValidatorWithAllowlist(cmds)` | Creates validator with custom allowlist |
| `ValidateCommand(cmd, allowWrite, useCustom)` | Main validation entry point |
| `extractBaseCommand(cmd)` | Extracts base command, handling paths and env vars |
| `extractCommandAfterSudo(cmd)` | Recursively extracts inner sudo command |
| `containsWriteRedirect(cmd)` | Detects `>` and `>>` redirects |