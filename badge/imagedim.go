package badge

import (
	"encoding/binary"
	"fmt"
)

// ========== 内嵌图片尺寸探测 ==========
//
// 模组 logo 以 BLOB 存在 mod_logos 表里（当前全部是 webp）。要把 logo 内嵌进
// 徽章 SVG 并保持宽高比，需要它的像素尺寸；这里只解析容器头，不做完整解码。
//
// 失败时返回 ok=false，调用方退化为按正方形区域居中显示。

// imageDimensions 返回图片的像素宽高。
func imageDimensions(data []byte) (int, int, bool) {
	switch {
	case len(data) >= 16 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return webpDimensions(data)
	case len(data) >= 24 && string(data[0:8]) == "\x89PNG\r\n\x1a\n":
		// PNG: IHDR 紧跟在 8 字节签名之后，宽高各 4 字节大端。
		w := int(binary.BigEndian.Uint32(data[16:20]))
		h := int(binary.BigEndian.Uint32(data[20:24]))
		return w, h, w > 0 && h > 0
	case len(data) >= 4 && data[0] == 0xFF && data[1] == 0xD8:
		return jpegDimensions(data)
	}
	return 0, 0, false
}

// webpDimensions 解析 WebP 的三种容器格式。
func webpDimensions(data []byte) (int, int, bool) {
	if len(data) < 16 {
		return 0, 0, false
	}
	switch string(data[12:16]) {
	case "VP8 ":
		// 简单有损：帧头里 14 字节处的 3 字节小端宽高（各 14 位）。
		if len(data) < 30 {
			return 0, 0, false
		}
		w := int(binary.LittleEndian.Uint16(data[26:28]) & 0x3FFF)
		h := int(binary.LittleEndian.Uint16(data[28:30]) & 0x3FFF)
		return w, h, w > 0 && h > 0
	case "VP8L":
		// 无损：1 字节签名(0x2F) + 14 位宽 + 14 位高。
		if len(data) < 25 || data[20] != 0x2F {
			return 0, 0, false
		}
		bits := binary.LittleEndian.Uint32(data[21:25])
		w := int(bits&0x3FFF) + 1
		h := int((bits>>14)&0x3FFF) + 1
		return w, h, true
	case "VP8X":
		// 扩展格式：24 位小端画布宽高减一。
		if len(data) < 30 {
			return 0, 0, false
		}
		w := int(uint32(data[24])|uint32(data[25])<<8|uint32(data[26])<<16) + 1
		h := int(uint32(data[27])|uint32(data[28])<<8|uint32(data[29])<<16) + 1
		return w, h, true
	}
	return 0, 0, false
}

// jpegDimensions 扫描 JPEG 的 SOFn 段取宽高。
func jpegDimensions(data []byte) (int, int, bool) {
	for i := 2; i+9 < len(data); {
		if data[i] != 0xFF {
			i++
			continue
		}
		marker := data[i+1]
		// SOF0..SOF15，跳过 DHT(0xC4)/JPG(0xC8)/DAC(0xCC)。
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			h := int(binary.BigEndian.Uint16(data[i+5 : i+7]))
			w := int(binary.BigEndian.Uint16(data[i+7 : i+9]))
			return w, h, w > 0 && h > 0
		}
		if marker == 0xD8 || marker == 0xD9 || (marker >= 0xD0 && marker <= 0xD7) {
			i += 2
			continue
		}
		segLen := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if segLen < 2 {
			return 0, 0, false
		}
		i += 2 + segLen
	}
	return 0, 0, false
}

// describeDimensions 仅用于调试输出。
func describeDimensions(data []byte) string {
	w, h, ok := imageDimensions(data)
	if !ok {
		return "解析失败"
	}
	return fmt.Sprintf("%dx%d", w, h)
}
