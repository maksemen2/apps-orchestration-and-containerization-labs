[CmdletBinding()]
param(
    [string] $Context = 'kind-lab5',
    [string] $Namespace = 'shop',
    [string] $Release = 'shop',
    [ValidateRange(1, 86400)]
    [int] $DurationSeconds = 180,
    [ValidateRange(1, 3600)]
    [int] $IntervalSeconds = 6
)

$ErrorActionPreference = 'Stop'

function Get-ReadyShopWorkloads {
    $jsonLines = & kubectl --context $Context --request-timeout=10s --namespace $Namespace get pods `
        --selector "app.kubernetes.io/instance=$Release" --output json 2>$null
    if ($LASTEXITCODE -ne 0) {
        throw "Could not list pods in namespace '$Namespace' using context '$Context'."
    }

    $json = ($jsonLines -join [Environment]::NewLine) | ConvertFrom-Json
    @($json.items | Where-Object {
        $name = $_.metadata.labels.'app.kubernetes.io/name'
        $ready = $_.status.conditions | Where-Object { $_.type -eq 'Ready' -and $_.status -eq 'True' }
        ($name -eq 'api' -or $name -eq 'worker') -and
            $null -ne $ready -and $null -eq $_.metadata.deletionTimestamp
    })
}

$stopAt = [DateTime]::UtcNow.AddSeconds($DurationSeconds)
$random = [System.Random]::new()

while ([DateTime]::UtcNow -lt $stopAt) {
    $readyPods = @(Get-ReadyShopWorkloads)
    if ($readyPods.Count -gt 0) {
        $pod = $readyPods[$random.Next(0, $readyPods.Count)]
        $podName = [string] $pod.metadata.name
        $podIp = [string] $pod.status.podIP
        $nodeName = [string] $pod.spec.nodeName
        $timestamp = [DateTime]::UtcNow.ToString('o')

        $deleteOutput = & kubectl --context $Context --request-timeout=10s --namespace $Namespace delete pod $podName --wait=false 2>&1
        $deleteExitCode = $LASTEXITCODE

        Write-Output "$timestamp deleted=$podName ip=$podIp node=$nodeName"
        foreach ($line in $deleteOutput) {
            Write-Output ([string] $line)
        }
        if ($deleteExitCode -ne 0) {
            throw "kubectl delete failed for pod '$podName' (exit $deleteExitCode)."
        }
    }

    $remainingSeconds = ($stopAt - [DateTime]::UtcNow).TotalSeconds
    if ($remainingSeconds -gt 0) {
        Start-Sleep -Seconds ([Math]::Min($IntervalSeconds, [Math]::Ceiling($remainingSeconds)))
    }
}
