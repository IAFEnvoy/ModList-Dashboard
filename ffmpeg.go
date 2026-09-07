package main

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// ffmpegAvailable 表示启动时是否检测到可用的 ffmpeg 命令。
// 可用时，上传的模组 logo 会自动转为 webp 再存入数据库；不可用时直接保存原图。
var ffmpegAvailable bool

// initFFmpeg 在启动时探测 ffmpeg：先在 PATH 中查找，再实际执行 -version 确认可用。
func initFFmpeg() {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		ffmpegAvailable = false
		log.Printf("[FFmpeg] 未检测到 ffmpeg 命令，图片上传时将直接保存原图")
		return
	}
	if err := exec.Command(path, "-version").Run(); err != nil {
		ffmpegAvailable = false
		log.Printf("[FFmpeg] 检测到 %s 但执行失败，图片上传时将直接保存原图: %v", path, err)
		return
	}
	ffmpegAvailable = true
	log.Printf("[FFmpeg] 已检测到可用的 ffmpeg (%s)，图片上传时将自动转为 webp", path)
}

// convertImageToWebp 用 ffmpeg 把图片数据转为 webp。
// ext 是原图扩展名（不带点，如 png/jpg/gif），仅用于让 ffmpeg 识别输入格式。
// 转换失败时返回错误，调用方应回退为直接保存原图。
func convertImageToWebp(data []byte, ext string) ([]byte, error) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, err
	}
	inFile, err := os.CreateTemp("", "modlogo-in-*."+ext)
	if err != nil {
		return nil, err
	}
	inName := inFile.Name()
	defer os.Remove(inName)
	if _, err := inFile.Write(data); err != nil {
		_ = inFile.Close()
		return nil, err
	}
	if err := inFile.Close(); err != nil {
		return nil, err
	}

	outFile, err := os.CreateTemp("", "modlogo-out-*.webp")
	if err != nil {
		return nil, err
	}
	outName := outFile.Name()
	_ = outFile.Close()
	defer os.Remove(outName)

	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-y",
		"-i", inName, "-c:v", "libwebp", "-quality", "85", outName)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg 转换失败: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	outData, err := os.ReadFile(outName)
	if err != nil {
		return nil, err
	}
	if len(outData) == 0 {
		return nil, fmt.Errorf("ffmpeg 输出为空")
	}
	return outData, nil
}
