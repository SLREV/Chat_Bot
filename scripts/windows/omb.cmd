@echo off
rem omb.cmd - pembungkus Windows: jalankan omb.exe di folder yang sama.
rem Taruh omb.exe + omb.cmd bersebelahan, lalu klik dua kali omb.cmd.
setlocal
cd /d "%~dp0"
if not exist "omb.exe" (
  echo omb.exe tidak ditemukan di folder ini.
  echo Ambil dist\omb-windows-amd64.exe dari arsip, rename jadi omb.exe.
  pause
  exit /b 1
)
"%~dp0omb.exe" %*
if errorlevel 1 pause
