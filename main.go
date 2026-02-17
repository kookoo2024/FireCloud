package main

import (
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
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

// ===== 备课系统数据结构 =====
type SlideItem struct {
	Type        string  `json:"type"`        // "media", "text", "marker"
	Path        string  `json:"path"`        // 相对路径
	Content     string  `json:"content"`     // 文本内容或显示名称
	StartTime   float64 `json:"startTime"`   // 书签点开始时间（秒）
	MarkerLabel string  `json:"markerLabel"` // 书签点标签名
}

type Slide struct {
	Name     string                 `json:"name"`
	Template string                 `json:"template"`
	Slots    map[string]interface{} `json:"slots"`
}

type LessonPlan struct {
	Name    string  `json:"name"`
	Slides  []Slide `json:"slides"`
	Updated int64   `json:"updated"`
}

// 目录树节点
type TreeNode struct {
	Name     string     `json:"name"`
	Path     string     `json:"path"`
	IsDir    bool       `json:"isDir"`
	Tags     []string   `json:"tags"`
	Markers  []Marker   `json:"markers,omitempty"`
	Children []TreeNode `json:"children,omitempty"`
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

	// --- 备课系统 API ---
	mux.HandleFunc("/api/tags/getAll", handleGetAllTags)
	mux.HandleFunc("/api/tags/save", handleSaveFileTags)
	mux.HandleFunc("/api/lesson/save", handleSaveLesson)
	mux.HandleFunc("/api/lesson/list", handleListLessons)
	mux.HandleFunc("/api/lesson/get", handleGetLesson)
	mux.HandleFunc("/api/tree", handleGetTree)

	mux.HandleFunc("/lesson", func(w http.ResponseWriter, r *http.Request) {
		data, _ := staticFS.ReadFile("static/lesson.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(data)
	})

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
		name := e.Name()
		// 过滤：所有 .json 文件，以及以 . 开头的隐藏项
		if strings.HasSuffix(strings.ToLower(name), ".json") || strings.HasPrefix(name, ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{Name: name, IsDir: e.IsDir(), Size: info.Size()})
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

	markerDBPath := filepath.Join(rootDir, ".fire_markers.json")
	data, err := os.ReadFile(markerDBPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(MarkersResponse{Markers: []Marker{}})
		return
	}

	var db map[string][]Marker
	if err := json.Unmarshal(data, &db); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(MarkersResponse{Markers: []Marker{}})
		return
	}

	markers, ok := db[relPath]
	if !ok {
		markers = []Marker{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(MarkersResponse{Markers: markers})
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

	markerDBPath := filepath.Join(rootDir, ".fire_markers.json")

	// 读取现有数据库
	db := make(map[string][]Marker)
	data, err := os.ReadFile(markerDBPath)
	if err == nil {
		json.Unmarshal(data, &db)
	}

	// 修正：解析前端传来的结构化数据 (封装在 markers 字段中)
	var req MarkersResponse
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "解析标注格式错误", http.StatusBadRequest)
		return
	}

	// 更新并保存
	db[relPath] = req.Markers
	newData, err := json.Marshal(db)
	if err != nil {
		http.Error(w, "序列化失败", http.StatusInternalServerError)
		return
	}

	err = os.WriteFile(markerDBPath, newData, 0644)
	if err != nil {
		http.Error(w, "写入数据库失败", http.StatusInternalServerError)
		return
	}

	// 在 Windows 下将数据库文件设置为隐藏
	if runtime.GOOS == "windows" {
		exec.Command("attrib", "+h", markerDBPath).Run()
	}

	w.Write([]byte("OK"))
}

// ===== 备课系统 API 实现 =====

// 获取全量标签（合并书签索引）
func handleGetAllTags(w http.ResponseWriter, r *http.Request) {
	tagFile := filepath.Join(rootDir, ".fire_tags.json")
	markerFile := filepath.Join(rootDir, ".fire_markers.json")

	db := make(map[string][]string)

	// 1. 读取标签文件
	if data, err := os.ReadFile(tagFile); err == nil {
		json.Unmarshal(data, &db)
	}

	// 2. 读取书签文件（作为自动标签 "已标注"）
	if data, err := os.ReadFile(markerFile); err == nil {
		var mdb map[string]interface{}
		if err := json.Unmarshal(data, &mdb); err == nil {
			for path := range mdb {
				db[path] = append(db[path], "已标注")
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(db)
}

// 获取带标签的目录树
func handleGetTree(w http.ResponseWriter, r *http.Request) {
	tagFile := filepath.Join(rootDir, ".fire_tags.json")
	markerFile := filepath.Join(rootDir, ".fire_markers.json")

	tagDB := make(map[string][]string)
	if data, err := os.ReadFile(tagFile); err == nil {
		json.Unmarshal(data, &tagDB)
	}

	markerDB := make(map[string][]Marker)
	if data, err := os.ReadFile(markerFile); err == nil {
		json.Unmarshal(data, &markerDB)
		for path := range markerDB {
			tagDB[path] = append(tagDB[path], "已标注")
		}
	}

	tree := buildTree(rootDir, "", tagDB, markerDB)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tree)
}

func isMediaFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	mediaExts := map[string]bool{
		".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true,
		".mp4": true, ".webm": true, ".mkv": true, ".mov": true, ".avi": true,
	}
	return mediaExts[ext]
}

func buildTree(basePath, relPath string, tagDB map[string][]string, markerDB map[string][]Marker) []TreeNode {
	absPath := filepath.Join(basePath, filepath.FromSlash(relPath))
	entries, err := os.ReadDir(absPath)
	if err != nil {
		return nil
	}

	var nodes []TreeNode
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}

		childRelPath := name
		if relPath != "" {
			childRelPath = relPath + "/" + name
		}

		if e.IsDir() {
			children := buildTree(basePath, childRelPath, tagDB, markerDB)
			if len(children) > 0 {
				node := TreeNode{
					Name:     name,
					Path:     childRelPath,
					IsDir:    true,
					Tags:     tagDB[childRelPath],
					Children: children,
				}
				nodes = append(nodes, node)
			}
		} else {
			if !isMediaFile(name) {
				continue
			}
			node := TreeNode{
				Name:    name,
				Path:    childRelPath,
				IsDir:   false,
				Tags:    tagDB[childRelPath],
				Markers: markerDB[childRelPath],
			}
			nodes = append(nodes, node)
		}
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].IsDir != nodes[j].IsDir {
			return nodes[i].IsDir
		}
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})

	return nodes
}

// 保存文件标签
func handleSaveFileTags(w http.ResponseWriter, r *http.Request) {
	tagFile := filepath.Join(rootDir, ".fire_tags.json")
	var newTags map[string][]string
	if err := json.NewDecoder(r.Body).Decode(&newTags); err != nil {
		http.Error(w, "Bad JSON", 400)
		return
	}

	db := make(map[string][]string)
	if data, err := os.ReadFile(tagFile); err == nil {
		json.Unmarshal(data, &db)
	}

	for k, v := range newTags {
		if len(v) == 0 {
			delete(db, k)
		} else {
			db[k] = v
		}
	}

	data, _ := json.Marshal(db)
	os.WriteFile(tagFile, data, 0644)
	if runtime.GOOS == "windows" {
		exec.Command("attrib", "+h", tagFile).Run()
	}
	w.Write([]byte("OK"))
}

// 保存备课方案
func handleSaveLesson(w http.ResponseWriter, r *http.Request) {
	var plan LessonPlan
	if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
		http.Error(w, "Bad JSON", 400)
		return
	}
	if plan.Name == "" {
		http.Error(w, "Name required", 400)
		return
	}
	plan.Updated = time.Now().Unix()

	lessonDir := filepath.Join(rootDir, ".fire_lessons")
	os.MkdirAll(lessonDir, 0755)
	if runtime.GOOS == "windows" {
		exec.Command("attrib", "+h", lessonDir).Run()
	}

	fileName := filepath.Join(lessonDir, plan.Name+".json")
	data, _ := json.Marshal(plan)
	os.WriteFile(fileName, data, 0644)
	w.Write([]byte("OK"))
}

// 列出所有备课方案
func handleListLessons(w http.ResponseWriter, r *http.Request) {
	lessonDir := filepath.Join(rootDir, ".fire_lessons")
	entries, err := os.ReadDir(lessonDir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("[]"))
		return
	}
	var list []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			list = append(list, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(list)
}

// 获取特定备课方案
func handleGetLesson(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "Name required", 400)
		return
	}
	filePath := filepath.Join(rootDir, ".fire_lessons", name+".json")
	data, err := os.ReadFile(filePath)
	if err != nil {
		http.Error(w, "Not found", 404)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(data)
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

// ===== 生成托盘图标（16x16 像素 ICO 格式，Windows 兼容） =====
func createFireIcon() []byte {
	const sz = 16

	// 创建 32 位 RGBA 图像
	img := image.NewRGBA(image.Rect(0, 0, sz, sz))

	// 金黄色星星
	starColor := color.RGBA{255, 180, 0, 255}

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

	// 填充五角星（不填充背景，保持透明）
	for y := 0; y < sz; y++ {
		for x := 0; x < sz; x++ {
			if isInsidePolygon(float64(x)+0.5, float64(y)+0.5, points) {
				img.Set(x, y, starColor)
			}
		}
	}

	// 构建 ICO 格式
	// ICO 头: 6 字节
	// 图像目录: 16 字节
	// 图像数据: BMP 格式

	var ico bytes.Buffer

	// ICO 头
	ico.Write([]byte{0, 0}) // Reserved
	ico.Write([]byte{1, 0}) // Type: 1 = ICO
	ico.Write([]byte{1, 0}) // Number of images

	// 图像目录项
	ico.WriteByte(16)        // Width
	ico.WriteByte(16)        // Height
	ico.WriteByte(0)         // Color palette
	ico.WriteByte(0)         // Reserved
	ico.Write([]byte{1, 0})  // Color planes
	ico.Write([]byte{32, 0}) // Bits per pixel

	// BMP 数据大小 (40 字节头 + 16*16*4 字节像素数据)
	bmpDataSize := 40 + sz*sz*4
	sizeBytes := make([]byte, 4)
	sizeBytes[0] = byte(bmpDataSize)
	sizeBytes[1] = byte(bmpDataSize >> 8)
	sizeBytes[2] = byte(bmpDataSize >> 16)
	sizeBytes[3] = byte(bmpDataSize >> 24)
	ico.Write(sizeBytes)

	// 图像数据偏移 (6 + 16 = 22)
	offsetBytes := []byte{22, 0, 0, 0}
	ico.Write(offsetBytes)

	// BMP 信息头 (40 字节)
	ico.Write([]byte{40, 0, 0, 0}) // Header size
	ico.Write([]byte{16, 0, 0, 0}) // Width
	ico.Write([]byte{32, 0, 0, 0}) // Height (doubled for ICO)
	ico.Write([]byte{1, 0})        // Planes
	ico.Write([]byte{32, 0})       // Bits per pixel
	ico.Write([]byte{0, 0, 0, 0})  // Compression
	ico.Write([]byte{0, 0, 0, 0})  // Image size (can be 0 for uncompressed)
	ico.Write([]byte{0, 0, 0, 0})  // X pixels per meter
	ico.Write([]byte{0, 0, 0, 0})  // Y pixels per meter
	ico.Write([]byte{0, 0, 0, 0})  // Colors used
	ico.Write([]byte{0, 0, 0, 0})  // Important colors

	// 像素数据 (从下到上，从左到右)
	for y := sz - 1; y >= 0; y-- {
		for x := 0; x < sz; x++ {
			c := img.RGBAAt(x, y)
			ico.WriteByte(c.B)
			ico.WriteByte(c.G)
			ico.WriteByte(c.R)
			ico.WriteByte(c.A)
		}
	}

	return ico.Bytes()
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
