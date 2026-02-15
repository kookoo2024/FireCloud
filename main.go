package main

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"net"

	"github.com/getlantern/systray"
	"github.com/skip2/go-qrcode"
)

//go:embed static/*
var staticFS embed.FS

const (
	listenAddr = ":80"
	rootDir    = `D:\Fire`
)

var server *http.Server

// ===== 数据结构 =====
type FileInfo struct {
	Name  string `json:"name"`
	IsDir bool   `json:"isDir"`
	Size  int64  `json:"size"`
}
type ListResponse struct {
	Files []FileInfo `json:"files"`
	Path  string     `json:"path"`
}

// 视频书签数据结构
type Marker struct {
	Time  float64 `json:"time"`
	Label string  `json:"label"`
}
type MarkersResponse struct {
	Markers []Marker `json:"markers"`
}

func main() {
	os.MkdirAll(rootDir, 0755)
	systray.Run(onReady, onExit)
}

func onReady() {
	systray.SetIcon(createFireIcon())
	systray.SetTitle("FireCloud")
	systray.SetTooltip("FireCloud 教学云盘 · 运行中")

	mOpen := systray.AddMenuItem("🌐 打开浏览器", "打开管理页面")
	mDir := systray.AddMenuItem("📁 打开 D:\\Fire", "打开文件目录")
	systray.AddSeparator()
	mAutoStart := systray.AddMenuItemCheckbox("🚀 开机启动", "设置程序开机自动运行", isAutoStartEnabled())
	systray.AddSeparator()
	mInfo := systray.AddMenuItem("📡 "+getLocalIP()+listenAddr, "服务地址")
	mInfo.Disable()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("❌ 退出", "关闭服务并退出")

	go startServer()
	go func() {
		time.Sleep(500 * time.Millisecond)
		openBrowser(fmt.Sprintf("http://localhost%s", listenAddr))
	}()

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openBrowser(fmt.Sprintf("http://localhost%s", listenAddr))
			case <-mDir.ClickedCh:
				exec.Command("explorer", rootDir).Start()
			case <-mAutoStart.ClickedCh:
				if mAutoStart.Checked() {
					if disableAutoStart() {
						mAutoStart.Uncheck()
					}
				} else {
					if enableAutoStart() {
						mAutoStart.Check()
					}
				}
			case <-mQuit.ClickedCh:
				systray.Quit()
			}
		}
	}()
}

func onExit() {
	if server != nil {
		server.Close()
	}
	os.Exit(0)
}

// ===== Windows 开机启动管理 =====
const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`
const appName = "FireCloud"

func isAutoStartEnabled() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	cmd := exec.Command("reg", "query", "HKCU\\"+runKey, "/v", appName)
	return cmd.Run() == nil
}

func enableAutoStart() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	cmd := exec.Command("reg", "add", "HKCU\\"+runKey, "/v", appName, "/t", "REG_SZ", "/d", exe, "/f")
	return cmd.Run() == nil
}

func disableAutoStart() bool {
	cmd := exec.Command("reg", "delete", "HKCU\\"+runKey, "/v", appName, "/f")
	return cmd.Run() == nil
}

func startServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/share", handleShare)
	mux.HandleFunc("/api/list", handleList)
	mux.HandleFunc("/api/upload", handleUpload)
	mux.HandleFunc("/api/mkdir", handleMkdir)

	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/markers/get", handleGetMarkers)
	mux.HandleFunc("/api/markers/save", handleSaveMarkers)
	mux.HandleFunc("/files/", handleFileServe)
	mux.HandleFunc("/", handleMain)

	server = &http.Server{Addr: listenAddr, Handler: mux}
	server.ListenAndServe()
}

// ===== 主路由 =====
func handleMain(w http.ResponseWriter, r *http.Request) {
	urlPath := r.URL.Path
	if urlPath == "/" || urlPath == "" {
		manage := r.URL.Query().Get("manage")
		if manage != "1" {
			indexPath := filepath.Join(rootDir, "index.html")
			if _, err := os.Stat(indexPath); err == nil {
				http.ServeFile(w, r, indexPath)
				return
			}
		}
		serveEmbeddedIndex(w, r)
		return
	}
	cleanPath := cleanRelPath(urlPath)
	if cleanPath == "" {
		serveEmbeddedIndex(w, r)
		return
	}
	absPath := filepath.Join(rootDir, filepath.FromSlash(cleanPath))
	if !isPathSafe(absPath) {
		http.Error(w, "禁止访问", http.StatusForbidden)
		return
	}
	info, err := os.Stat(absPath)
	if err != nil {
		serveEmbeddedIndex(w, r)
		return
	}
	if info.IsDir() {
		manage := r.URL.Query().Get("manage")
		if manage != "1" {
			indexPath := filepath.Join(absPath, "index.html")
			if _, err := os.Stat(indexPath); err == nil {
				http.StripPrefix(urlPath, http.FileServer(http.Dir(absPath))).ServeHTTP(w, r)
				return
			}
		}
		serveEmbeddedIndex(w, r)
	} else {
		http.ServeFile(w, r, absPath)
	}
}

func serveEmbeddedIndex(w http.ResponseWriter, r *http.Request) {
	data, _ := staticFS.ReadFile("static/index.html")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(data)
}

// ===== API =====
func handleList(w http.ResponseWriter, r *http.Request) {
	relPath := cleanRelPath(r.URL.Query().Get("path"))
	absPath := filepath.Join(rootDir, filepath.FromSlash(relPath))
	if !isPathSafe(absPath) {
		http.Error(w, "禁止访问", http.StatusForbidden)
		return
	}
	entries, err := os.ReadDir(absPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ListResponse{Files: []FileInfo{}, Path: relPath})
		return
	}
	var files []FileInfo
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".marks.json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{Name: e.Name(), IsDir: e.IsDir(), Size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].IsDir != files[j].IsDir {
			return files[i].IsDir
		}
		return strings.ToLower(files[i].Name) < strings.ToLower(files[j].Name)
	})
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ListResponse{Files: files, Path: relPath})
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	relPath := cleanRelPath(r.URL.Query().Get("path"))
	if relPath == "" {
		http.Error(w, "缺少路径参数", http.StatusBadRequest)
		return
	}
	absPath := filepath.Join(rootDir, filepath.FromSlash(relPath))
	if !isPathSafe(absPath) {
		http.Error(w, "禁止访问", http.StatusForbidden)
		return
	}
	os.MkdirAll(filepath.Dir(absPath), 0755)
	outFile, err := os.Create(absPath)
	if err != nil {
		http.Error(w, "创建文件失败", http.StatusInternalServerError)
		return
	}
	defer outFile.Close()
	io.Copy(outFile, r.Body)
	w.Write([]byte("OK"))
}

func handleMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	relPath := cleanRelPath(r.URL.Query().Get("path"))
	if relPath == "" {
		http.Error(w, "缺少路径参数", http.StatusBadRequest)
		return
	}
	absPath := filepath.Join(rootDir, filepath.FromSlash(relPath))
	if !isPathSafe(absPath) {
		http.Error(w, "禁止访问", http.StatusForbidden)
		return
	}
	os.MkdirAll(absPath, 0755)
	w.Write([]byte("OK"))
}

func handleFileServe(w http.ResponseWriter, r *http.Request) {
	relPath := strings.TrimPrefix(r.URL.Path, "/files/")
	relPath = cleanRelPath(relPath)
	if relPath == "" {
		http.Error(w, "路径无效", http.StatusBadRequest)
		return
	}
	absPath := filepath.Join(rootDir, filepath.FromSlash(relPath))
	if !isPathSafe(absPath) {
		http.Error(w, "禁止访问", http.StatusForbidden)
		return
	}
	http.ServeFile(w, r, absPath)
}

// 服务状态 API
func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ip := getLocalIP()
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "running",
		"address": ip + listenAddr,
		"ip":      ip,
		"rootDir": rootDir,
	})
}

// 视频书签 API
func handleGetMarkers(w http.ResponseWriter, r *http.Request) {
	relPath := cleanRelPath(r.URL.Query().Get("path"))
	if relPath == "" {
		http.Error(w, "缺少路径参数", http.StatusBadRequest)
		return
	}
	absPath := filepath.Join(rootDir, filepath.FromSlash(relPath))
	if !isPathSafe(absPath) {
		http.Error(w, "禁止访问", http.StatusForbidden)
		return
	}

	marksPath := absPath + ".marks.json"
	data, err := os.ReadFile(marksPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(MarkersResponse{Markers: []Marker{}})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
}

func handleSaveMarkers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	relPath := cleanRelPath(r.URL.Query().Get("path"))
	if relPath == "" {
		http.Error(w, "缺少路径参数", http.StatusBadRequest)
		return
	}
	absPath := filepath.Join(rootDir, filepath.FromSlash(relPath))
	if !isPathSafe(absPath) {
		http.Error(w, "禁止访问", http.StatusForbidden)
		return
	}

	marksPath := absPath + ".marks.json"
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusInternalServerError)
		return
	}

	err = os.WriteFile(marksPath, body, 0644)
	if err != nil {
		http.Error(w, "写入标注失败", http.StatusInternalServerError)
		return
	}

	// 在 Windows 下将标注文件设置为隐藏
	if runtime.GOOS == "windows" {
		exec.Command("attrib", "+h", marksPath).Run()
	}

	w.Write([]byte("OK"))
}

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil && strings.HasPrefix(ipnet.IP.String(), "192.168.") {
				return ipnet.IP.String()
			}
		}
	}
	// 如果没找到 192.168 开头的，返回第一个非回环 IPv4
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// ===== 安全工具 =====
func cleanRelPath(p string) string {
	p = filepath.ToSlash(p)
	p = strings.Trim(p, "/ ")
	p = filepath.Clean(p)
	p = filepath.ToSlash(p)
	parts := strings.Split(p, "/")
	var clean []string
	for _, part := range parts {
		if part == ".." || part == "." || part == "" {
			continue
		}
		clean = append(clean, part)
	}
	return strings.Join(clean, "/")
}

func isPathSafe(absPath string) bool {
	return strings.HasPrefix(strings.ToLower(filepath.Clean(absPath)), strings.ToLower(filepath.Clean(rootDir)))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Start()
}

// ===== 生成托盘图标（标准的 16x16 像素，提高 Windows 兼容性） =====
func createFireIcon() []byte {
	const sz = 16
	img := image.NewRGBA(image.Rect(0, 0, sz, sz))

	// 金黄色星星
	starColor := color.RGBA{255, 215, 0, 255}

	cx, cy := float64(sz)/2, float64(sz)/2

	// 生成五角星的顶点坐标
	points := make([][2]float64, 10)
	outerRadius := float64(sz) * 0.45
	innerRadius := outerRadius * 0.4

	for i := 0; i < 10; i++ {
		angle := float64(i)*36.0 - 90.0
		rad := angle * 3.14159265359 / 180.0
		var r float64
		if i%2 == 0 {
			r = outerRadius
		} else {
			r = innerRadius
		}
		points[i][0] = cx + r*float64(cos(rad))
		points[i][1] = cy + r*float64(sin(rad))
	}

	for y := 0; y < sz; y++ {
		for x := 0; x < sz; x++ {
			if isInsidePolygon(float64(x), float64(y), points) {
				img.Set(x, y, starColor)
			} else {
				img.Set(x, y, color.Transparent)
			}
		}
	}

	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

// 简单的三角函数实现
func cos(rad float64) float64 {
	// 使用泰勒级数近似
	x := rad
	result := 1.0
	term := 1.0
	for i := 1; i <= 10; i++ {
		term *= -x * x / float64(2*i*(2*i-1))
		result += term
	}
	return result
}

func sin(rad float64) float64 {
	// 使用泰勒级数近似
	x := rad
	result := x
	term := x
	for i := 1; i <= 10; i++ {
		term *= -x * x / float64(2*i*(2*i+1))
		result += term
	}
	return result
}

// 判断点是否在多边形内（射线法）
func isInsidePolygon(x, y float64, points [][2]float64) bool {
	n := len(points)
	inside := false

	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := points[i][0], points[i][1]
		xj, yj := points[j][0], points[j][1]

		if ((yi > y) != (yj > y)) && (x < (xj-xi)*(y-yi)/(yj-yi)+xi) {
			inside = !inside
		}
		j = i
	}

	return inside
}

func handleShare(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "Path required", 400)
		return
	}

	// Encode path segments properly
	parts := strings.Split(path, "/")
	var encodedParts []string
	for _, p := range parts {
		encodedParts = append(encodedParts, url.PathEscape(p))
	}
	encodedPath := strings.Join(encodedParts, "/")

	// Use r.Host to respect the actual hostname/IP used by the client
	fullURL := fmt.Sprintf("http://%s/files/%s", r.Host, encodedPath)

	png, err := qrcode.Encode(fullURL, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "QR Generation failed", 500)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"url": fullURL,
		"qr":  base64.StdEncoding.EncodeToString(png),
	})
}
