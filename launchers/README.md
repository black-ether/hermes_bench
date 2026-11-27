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
