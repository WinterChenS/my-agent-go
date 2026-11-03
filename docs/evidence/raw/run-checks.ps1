$base = $env:PATH
$good = "D:\Program Files\nodejs;D:\project\my-agent-go\.tools\bin;D:\Program Files\Go\bin"
$fake = "D:\project\my-agent-go\build\fake-node"
$script = "D:\project\my-agent-go\scripts\check-toolchain.ps1"

function RunCheck($name, $extraPath, $log) {
  Write-Output "===== $name =====" | Out-File -FilePath $log -Encoding utf8
  $env:PATH = "$extraPath;$base"
  # resolve sanity
  Add-Content -Path $log -Value ("node  -> " + (Get-Command node).Source)
  Add-Content -Path $log -Value ("npm  -> " + (Get-Command npm).Source)
  Add-Content -Path $log -Value ("go   -> " + (Get-Command go).Source)
  Add-Content -Path $log -Value ("wails3 -> " + (Get-Command wails3).Source)
  & $script 6>&1 2>&1 | ForEach-Object { Add-Content -Path $log -Value $_ }
  Add-Content -Path $log -Value "EXIT=$LASTEXITCODE"
}

# Normal (correctly configured) environment: node 24 first.
RunCheck "NORMAL_ENV" $good "D:\project\my-agent-go\build\check-normal.log"

# Fake v16 environment: WeChat-style node grabs first PATH slot.
RunCheck "FAKE_V16_ENV" "$fake;$good" "D:\project\my-agent-go\build\check-fakev16.log"
