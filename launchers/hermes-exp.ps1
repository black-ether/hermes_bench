$driver = Join-Path $env:USERPROFILE 'scripts\hermes_bench_v0_8_source\launchers\experimental.ps1'
& $driver -Command 'hermes' -CommandArgs $args
exit $LASTEXITCODE
