@echo off
REM ========================================
REM FireCloud 编译脚本
REM -s -w    : 去除调试信息，减小体积
REM -H windowsgui : 隐藏 CMD 黑框
REM CGO_ENABLED=0 : 纯 Go 编译，增加兼容性
REM ========================================

SET CGO_ENABLED=0
SET GOOS=windows
SET GOARCH=amd64

echo [FireCloud] 开始编译...
go build -ldflags "-s -w -H windowsgui" -o FireCloud.exe main.go

if %ERRORLEVEL% equ 0 (
    echo [FireCloud] 编译成功！
    echo [FireCloud] 输出: FireCloud.exe
    echo.
    echo 双击 FireCloud.exe 即可启动
    echo 默认地址: http://localhost:8080
    echo 账号: admin  密码: fire2026
) else (
    echo [FireCloud] 编译失败，请检查 Go 环境
)

pause
