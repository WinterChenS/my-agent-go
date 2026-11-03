@echo off
rem Simulated "WeChat devtools" node that grabs PATH precedence.
rem Only the toolchain probe (-v/--version) is faked to v16; anything else
rem delegates to the real baseline node so npm/go/wails keep working.
if "%1"=="-v" (echo v16.0.0 & exit /b 0)
if "%1"=="--version" (echo v16.0.0 & exit /b 0)
"D:\Program Files\nodejs\node.exe" %*
