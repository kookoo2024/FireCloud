package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"net"

	"github.com/fyne-io/systray"
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
	mInfo := systray.AddMenuItem("📡 "+getLocalIP()+listenAddr, "服务地址")
	mInfo.Disable()
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("❌ 退出 FireCloud", "关闭服务并退出")

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

func startServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/list", handleList)
	mux.HandleFunc("/api/upload", handleUpload)
	mux.HandleFunc("/api/delete", handleDelete)
	mux.HandleFunc("/api/mkdir", handleMkdir)

	mux.HandleFunc("/api/status", handleStatus)
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

func handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "方法不允许", http.StatusMethodNotAllowed)
		return
	}
	relPath := cleanRelPath(r.URL.Query().Get("path"))
	if relPath == "" {
		http.Error(w, "缺少路径参数", http.StatusBadRequest)
		return
	}
	absPath := filepath.Join(rootDir, filepath.FromSlash(relPath))
	if !isPathSafe(absPath) || absPath == filepath.Clean(rootDir) {
		http.Error(w, "禁止操作", http.StatusForbidden)
		return
	}
	os.RemoveAll(absPath)
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

// ===== 生成托盘图标（32x32 字母 T 样式 PNG） =====
func createFireIcon() []byte {
	const sz = 32
	img := image.NewRGBA(image.Rect(0, 0, sz, sz))

	// 背景颜色：深紫色
	bgColor := color.RGBA{124, 106, 255, 255}
	// 文字颜色：白色
	fgColor := color.RGBA{255, 255, 255, 255}

	// 填充圆角矩形背景 (使用圆角效果避免生硬)
	c := float64(sz) / 2
	for y := 0; y < sz; y++ {
		for x := 0; x < sz; x++ {
			dx, dy := float64(x)-c, float64(y)-c
			if dx*dx+dy*dy <= c*c {
				img.Set(x, y, bgColor)
			}
		}
	}

	// 绘制字母 T (居中并加粗)
	// 横杠
	for x := 7; x <= 25; x++ {
		for y := 7; y <= 11; y++ {
			img.Set(x, y, fgColor)
		}
	}
	// 竖杠
	for x := 13; x <= 19; x++ {
		for y := 12; y <= 25; y++ {
			img.Set(x, y, fgColor)
		}
	}

	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}
