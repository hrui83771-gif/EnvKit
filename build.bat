@echo off
chcp 65001 >nul
setlocal
set GOOS=windows
set GOARCH=amd64
for /f %%i in ('powershell -NoProfile -Command "Get-Date -Format yyyy-MM-dd"') do set BUILD_DATE=%%i
echo 正在编译 EnvKit.exe ...
rem 版本号以 main.go 的 appVersion 为准（单一来源，勿在此写死）；这里只注入构建日期
rem -trimpath：抹掉编译机上的源码绝对路径（否则 exe 里会残留 D:\...\envkit\*.go 这类路径）
rem 内嵌默认配置来自 config.dist.json（干净模板），config.json 只作本机实配、不参与分发
go build -trimpath -ldflags "-s -w -X main.buildDate=%BUILD_DATE%" -o EnvKit.exe .
if errorlevel 1 (
    echo 编译失败，请确认已安装 Go 1.21+ 并在 PATH 中。
    exit /b 1
)
echo 编译完成: EnvKit.exe
echo.
echo 说明：图标与文件属性（版本信息）已通过 resource.syso 编译进 exe，无需安装工具即可直接 go build。
echo 升级版本号时（改 main.go 的 appVersion 后）同步更新 versioninfo.json 并重新生成资源：
echo   go run github.com/josephspurrier/goversioninfo/cmd/goversioninfo@latest
echo 可选：用 upx 进一步压缩体积（约 6MB -^> ~2MB）：
echo   upx --best --lzma EnvKit.exe
endlocal
