# Used by the two PowerShell entry points. Environment changes are process-local.
param([string]$Command, [string[]]$CommandArgs)
$ErrorActionPreference = 'Stop'
$checkout = Split-Path $PSScriptRoot
$project = Join-Path $env:USERPROFILE 'scripts\context-prep'
$experiment = Join-Path $project '.venv\hermes-windows-profile'
$bin = Join-Path $experiment 'bin'
$logs = Join-Path $experiment 'experiments'
$config = Join-Path $checkout 'reports\context-prep-experimental\bench.json'
$settings = @{
    HERMES_HOME = $experiment
    HERMES_PROFILE = $null
    HERMES_PROFILE_NAME = $null
    HERMES_CONFIG_PATH = Join-Path $experiment 'config.yaml'
    HERMES_ENV_PATH = Join-Path $experiment '.env'
    HERMES_CONTEXT_PREP_CLI = Join-Path $project '.venv\Scripts\context-prep.exe'
    HERMES_CONTEXT_PREP_REPO = Join-Path $env:USERPROFILE 'scripts\task-reference'
    HERMES_CONTEXT_PREP_RECORDS = Join-Path $project 'data\records\records_r3.json'
    HERMES_CONTEXT_PREP_MODEL_DIR = Join-Path $project 'models\bge-small'
    HERMES_CONTEXT_PREP_INDEX_DIR = Join-Path $project 'data\index'
    HERMES_CONTEXT_PREP_LOG = Join-Path $logs 'hermes-context-prep.jsonl'
    HERMES_CONTEXT_PREP_ARTIFACTS = Join-Path $logs 'hermes-context-prep'
    CODEGOS_TELEMETRY_DIR = $logs
    PATH = "$bin;$env:PATH"
}
$prior = @{}
try {
    foreach ($key in $settings.Keys) {
        $prior[$key] = [Environment]::GetEnvironmentVariable($key, 'Process')
        [Environment]::SetEnvironmentVariable($key, $settings[$key], 'Process')
    }
    if ($Command -eq 'bench') {
        & (Join-Path $checkout 'hermes-bench.exe') --config $config @CommandArgs
    } else {
        & (Join-Path $bin 'hermes.exe') @CommandArgs
    }
    $nativeExit = $LASTEXITCODE
} finally {
    foreach ($key in $prior.Keys) {
        [Environment]::SetEnvironmentVariable($key, $prior[$key], 'Process')
    }
}
exit $nativeExit
