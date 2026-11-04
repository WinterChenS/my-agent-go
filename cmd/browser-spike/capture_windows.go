//go:build windows

package main

// 本文件是 B01a 窗口级截图的备用手段（辅助证据）。
//
// 硬证据是 CDP 的 Page.captureScreenshot（页面像素）；窗口级截图只用来展示
// “网页确实在主窗口右侧那块位置”，并验证应用内 dialog 的遮挡关系。本机的
// WebView2 用 DirectComposition 合成，PrintWindow 常常拿不到内容，所以这里
// 保留两条路：先 PrintWindow(PW_RENDERFULLCONTENT)，失败/纯色再退回屏幕 BitBlt。
// 哪条成功、是否拍到合成层，都原样写进证据，不伪造。

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	capUser32 = windows.NewLazySystemDLL("user32.dll")
	capGdi32  = windows.NewLazySystemDLL("gdi32.dll")

	procGetWindowRect   = capUser32.NewProc("GetWindowRect")
	procGetDC           = capUser32.NewProc("GetDC")
	procReleaseDC       = capUser32.NewProc("ReleaseDC")
	procPrintWindow     = capUser32.NewProc("PrintWindow")
	procSetForeground   = capUser32.NewProc("SetForegroundWindow")
	procIsWindowVisible = capUser32.NewProc("IsWindowVisible")

	procCreateCompatibleDC = capGdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection   = capGdi32.NewProc("CreateDIBSection")
	procSelectObject       = capGdi32.NewProc("SelectObject")
	procBitBlt             = capGdi32.NewProc("BitBlt")
	procDeleteObject       = capGdi32.NewProc("DeleteObject")
	procDeleteDC           = capGdi32.NewProc("DeleteDC")
)

type capRect struct{ Left, Top, Right, Bottom int32 }

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
}

const (
	pwRenderFullContent = 0x00000002
	captureSrcCopy      = 0x00CC0020
	dibRGBColors        = 0
)

// windowVisible 是 spike 用来复核 SetVisible 的实测值。
func windowVisible(hwnd uintptr) bool {
	v, _, _ := procIsWindowVisible.Call(hwnd)
	return v != 0
}

// captureWindow 截取指定顶层窗口。
//
// mode 有两种：
//   - "print"：PrintWindow(PW_RENDERFULLCONTENT)。**包含不了子窗口 HWND** 的内容，
//     所以看不到原生 WebView2（实测：右栏还是主 WebView 的占位 UI）；
//   - "screen"：把窗口抬到前台后用屏幕 DC BitBlt。能看到子窗口，但要求窗口
//     真的没被盖住。
//
// 两条都要跑并如实报告：B01a 的“右栏是网页”硬证据只能靠 CDP 页面截图。
func captureWindow(hwnd uintptr, mode string) (image.Image, string, error) {
	var wr capRect
	if ok, _, e := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr))); ok == 0 {
		return nil, "", fmt.Errorf("GetWindowRect 失败: %v", e)
	}
	w := int(wr.Right - wr.Left)
	h := int(wr.Bottom - wr.Top)
	if w <= 0 || h <= 0 {
		return nil, "", fmt.Errorf("窗口尺寸非法 %dx%d", w, h)
	}

	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return nil, "", fmt.Errorf("GetDC(NULL) 失败")
	}
	defer func() { _, _, _ = procReleaseDC.Call(0, screenDC) }()

	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return nil, "", fmt.Errorf("CreateCompatibleDC 失败")
	}
	defer func() { _, _, _ = procDeleteDC.Call(memDC) }()

	// 用 DIB section 而不是 CreateCompatibleBitmap + GetDIBits：
	// 后者在本机一直返回 0 行（LastError=0），拿不到像素；DIB section 的像素
	// 直接就在 bits 指针里，少一次转换也少一个坑。
	bi := bitmapInfoHeader{
		Size:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		Width:       int32(w),
		Height:      -int32(h), // 负高度 = 自上而下
		Planes:      1,
		BitCount:    32,
		Compression: dibRGBColors,
	}
	var bits unsafe.Pointer
	// 参数顺序：hdc, pbmi, usage, ppvBits, hSection, offset。
	bmp, _, e := procCreateDIBSection.Call(screenDC,
		uintptr(unsafe.Pointer(&bi)), dibRGBColors, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == nil {
		return nil, "", fmt.Errorf("CreateDIBSection 失败: %v", e)
	}
	defer func() { _, _, _ = procDeleteObject.Call(bmp) }()
	old, _, _ := procSelectObject.Call(memDC, bmp)

	method := "printwindow"
	if mode == "screen" {
		method = "screen-bitblt"
		_, _, _ = procSetForeground.Call(hwnd)
		time.Sleep(300 * time.Millisecond)
		if ok, _, e := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), screenDC,
			uintptr(wr.Left), uintptr(wr.Top), captureSrcCopy); ok == 0 {
			return nil, "", fmt.Errorf("BitBlt(屏幕) 失败: %v", e)
		}
	} else {
		ok, _, e := procPrintWindow.Call(hwnd, memDC, pwRenderFullContent)
		if ok == 0 {
			return nil, "", fmt.Errorf("PrintWindow(PW_RENDERFULLCONTENT) 失败: %v", e)
		}
	}
	_, _, _ = procSelectObject.Call(memDC, old)

	buf := unsafe.Slice((*byte)(bits), w*h*4)
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*w + x) * 4
			// DIB 是 BGRA
			img.SetRGBA(x, y, color.RGBA{R: buf[i+2], G: buf[i+1], B: buf[i+0], A: 255})
		}
	}
	return img, method, nil
}

// imageStats 是"这张图是不是纯色/空白"的可复核统计。
type imageStats struct {
	Width         int
	Height        int
	UniqueColors  int
	DominantColor string
	DominantRatio float64
	Samples       int
}

// statsOfImage 按固定步长采样，统计颜色数与主色占比。
//
// 纯色图的 UniqueColors=1、DominantRatio=1；真实页面的两者都不会是这样。
func statsOfImage(img image.Image) imageStats {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	counts := map[color.RGBA]int{}
	samples := 0
	stepX, stepY := w/64, h/64
	if stepX < 1 {
		stepX = 1
	}
	if stepY < 1 {
		stepY = 1
	}
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			r, g, bl, _ := img.At(x, y).RGBA()
			counts[color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(bl >> 8), A: 255}]++
			samples++
		}
	}
	best := color.RGBA{}
	bestN := 0
	for c, n := range counts {
		if n > bestN {
			best, bestN = c, n
		}
	}
	ratio := 0.0
	if samples > 0 {
		ratio = float64(bestN) / float64(samples)
	}
	return imageStats{
		Width:         w,
		Height:        h,
		UniqueColors:  len(counts),
		DominantColor: fmt.Sprintf("#%02x%02x%02x", best.R, best.G, best.B),
		DominantRatio: ratio,
		Samples:       samples,
	}
}

func (s imageStats) String() string {
	return fmt.Sprintf("w=%d h=%d samples=%d uniqueColors=%d dominant=%s dominantRatio=%.3f",
		s.Width, s.Height, s.Samples, s.UniqueColors, s.DominantColor, s.DominantRatio)
}

// Uniform 判断这张图是不是“没有可信内容的纯色图”。
//
// 阈值取“采样到的颜色数 ≤4”：纯色/空白就是 1；真实页面哪怕只有一行字也会因为
// 抗锯齿出现多个颜色。主色占比只当诊断值，不拿它判死——白底页面本来就
// 大部分是白色。
func (s imageStats) Uniform() bool { return s.UniqueColors <= 4 }

// savePNG 把图片写到磁盘。
func savePNG(path string, img image.Image) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}

func dirOf(path string) string {
	return filepath.Dir(path)
}

// describeChildren 按 z 序（上→下）列出指定窗口的直接子窗口。
//
// B01a 用它证明一件事：原生 WebView2 子窗口必须排在 Wails 主 WebView 的
// 子窗口之上，否则页面会被主 WebView 盖住（窗口截图看不到、用户也看不到）。
func describeChildren(hwnd uintptr) []string {
	const (
		gwChild    = 5
		gwHwndNext = 2
	)
	var out []string
	proc := capUser32.NewProc("GetWindow")
	procGetWindowText := capUser32.NewProc("GetWindowTextW")
	child, _, _ := proc.Call(hwnd, gwChild)
	for i := 0; child != 0 && i < 16; i++ {
		name := make([]uint16, 256)
		n, _, _ := procGetClassNameW.Call(child, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
		cls := windows.UTF16ToString(name[:n])
		title := make([]uint16, 128)
		tn, _, _ := procGetWindowText.Call(child, uintptr(unsafe.Pointer(&title[0])), uintptr(len(title)))
		out = append(out, fmt.Sprintf("0x%x class=%q title=%q", child, cls, windows.UTF16ToString(title[:tn])))
		child, _, _ = proc.Call(child, gwHwndNext)
	}
	return out
}

var procGetClassNameW = capUser32.NewProc("GetClassNameW")
