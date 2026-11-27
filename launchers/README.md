# Experimental Hermes commands on this Windows machine

The installed entry points live in `%USERPROFILE%\scripts`, which is on the user PATH. Each calls the shared driver in this checkout. The driver sets the experimental home, Context Prep CLI and data paths, telemetry directory and Hermes executable search path for the native command and its children. It restores the caller's environment on return. Bench uses the checkout's maintained `hermes-bench.exe` with `reports\context-prep-experimental\bench.json`. Ordinary Hermes and Bench keep their standard settings.

## Daily use

```powershell
hermes-exp plugins list --plain --no-bundled
hermes-exp sessions list
bench-exp list
bench-exp report 1
```

After a new measured session, run `bench-exp run` for incremental sync and the latest saved report. Use `bench-exp sync --all` to import stored sessions lacking telemetry. Numbers refer to the current Bench listing. Start an interactive experimental session with `hermes-exp` from the intended repository. To inspect injection and timing logs:

```powershell
$exp = "$env:USERPROFILE\scripts\context-prep\.venv\hermes-windows-profile"
Get-Content "$exp\experiments\hermes-context-prep.jsonl" -Tail 5
Get-Content "$exp\experiments\events.jsonl" -Tail 10
```

Use an ordinary PowerShell process without a manual `HERMES_HOME` override for standard `hermes` and `hermes desktop`. To undo a manual override in the current tab, run `$env:HERMES_HOME = $null`. A child pane can inherit a manually activated shell's environment. The experimental launchers select the isolated home on every invocation and restore the caller's previous values.

Resume an exact ID from `hermes-exp sessions list` with `hermes-exp --resume <session-id>`. The separate top-level `resume` subcommand lifts an emergency stop. An explicit ID also avoids the recent-session and terminal breadcrumb rules used by bare `--continue`.

Launch experimental Desktop with `hermes-exp desktop --skip-build --cwd "$env:USERPROFILE\scripts\task-reference"`. Use `--skip-build` when the existing unpacked app is installed. The launcher gives experimental Electron its own user-data directory under the experimental home. This isolates its single-instance lock and saved connections. In the experimental window, select **This device / default** to inspect that local store.

To check the effective shell override and latest experimental injection:

```powershell
Get-Item Env:HERMES_HOME -ErrorAction SilentlyContinue
$exp = "$env:USERPROFILE\scripts\context-prep\.venv\hermes-windows-profile"
Get-Item "$exp\state.db", "$exp\experiments\events.jsonl"
hermes-exp plugins list --plain --no-bundled
Get-Content "$exp\experiments\hermes-context-prep.jsonl" -Tail 25 |
    ForEach-Object { $_ | ConvertFrom-Json } |
    Where-Object status |
    Select-Object -Last 1 ts, session_id, status, injected_chars, hook_ms
bench-exp list
```

## Gateway lifecycle

The experimental configuration sets `gateway.standalone: true`. For a persistent interactive gateway, run `hermes-exp gateway run` in a dedicated terminal and leave it open. To reload plugins or change child environment settings, stop that experimental foreground process with Ctrl+C, then run the same command again. Use `hermes-exp gateway status` to inspect it. The standard gateway has a different home. A gateway already running before a configuration change retains its launch environment. No system service was installed. At setup the isolated gateway recorded a stopped state and its PID was absent.

## Installation on this checkout

The paths below assume this user's existing Context Prep installation, Bench configurations, and repair checkout. The standard plugins directory contains the compatible codegos-telemetry 0.2.0 distribution. To install into a fresh copy of the experimental home, create `$exp\plugins\codegos-telemetry`, copy its `__init__.py` and `plugin.yaml` from `$env:LOCALAPPDATA\hermes\plugins\codegos-telemetry`, and add `codegos-telemetry` alongside `hermes-context-prep` in the existing experimental config.yaml plugins.enabled list. Preserve other settings and back up the config before editing. The launcher pins CODEGOS_TELEMETRY_DIR to the experimental experiments directory. Its Bench JSON uses that directory's events.jsonl, an isolated sessions directory and an isolated reports directory.

To reinstall the launchers:

```powershell
$benchRoot = "$env:USERPROFILE\scripts\hermes_bench_v0_8_source"
Copy-Item "$benchRoot\launchers\hermes-exp.ps1","$benchRoot\launchers\bench-exp.ps1" -Destination "$env:USERPROFILE\scripts"
Get-Command hermes-exp,bench-exp
```

## Removal and rollback

Stop the experimental foreground gateway first if running. Remove only the two installed entry points:

```powershell
Remove-Item -LiteralPath "$env:USERPROFILE\scripts\hermes-exp.ps1","$env:USERPROFILE\scripts\bench-exp.ps1"
```

To deactivate telemetry, remove only `codegos-telemetry` from plugins.enabled in the experimental config and relaunch the experimental gateway when needed. The logs and archives can stay in place. Setup backups are in `reports\D02-install-backup`. To roll back the binary, close running Bench commands, then copy `hermes-bench-0991bc7.exe` from that folder over the checkout's `hermes-bench.exe`. The standard config was preserved byte-for-byte.
