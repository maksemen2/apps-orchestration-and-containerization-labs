[CmdletBinding()]
param(
    [string] $Context = 'kind-lab5',
    [string] $Namespace = 'shop',
    [string] $Release = 'shop'
)

$ErrorActionPreference = 'Stop'

$chartPath = (Resolve-Path (Join-Path $PSScriptRoot '..\charts\shop')).Path
$faults = @(
    [pscustomobject]@{ Key = 'networkPolicy.dns.port'; Value = '54' },
    [pscustomobject]@{ Key = 'networkPolicy.databaseEgressPorts.api'; Value = '5433' },
    [pscustomobject]@{ Key = 'networkPolicy.databaseEgressPorts.worker'; Value = '5433' },
    [pscustomobject]@{ Key = 'networkPolicy.databaseIngressLabels.api'; Value = 'api-broken' }
)
$fault = $faults[(Get-Random -Minimum 0 -Maximum $faults.Count)]

$helmArguments = @(
    'upgrade', $Release, $chartPath,
    '--kube-context', $Context,
    '--namespace', $Namespace,
    '--reuse-values',
    '--set', "$($fault.Key)=$($fault.Value)"
)
$helmOutput = & helm @helmArguments 2>&1
$helmExitCode = $LASTEXITCODE

if ($helmExitCode -ne 0) {
    $helmOutput | Write-Output
    Write-Error "Helm could not apply the network fault to release '$Release' in namespace '$Namespace' (exit $helmExitCode)."
    exit $helmExitCode
}

Write-Output "$([DateTime]::UtcNow.ToString('o')) network fault applied to release=$Release namespace=$Namespace"
