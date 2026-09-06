package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func loadConfig(path string, v interface{}) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[!] 无法读取配置文件 %s: %v", path, err)
		return
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		log.Printf("[!] 解析配置文件 %s 失败: %v", path, err)
	}
}

// ensureConfigFiles 在缺少运行时配置文件时，从同目录 *.yml.example 模板复制一份，
// 保证全新环境首次启动不会因缺少本地配置而不可用。只补缺失文件，不覆盖已有配置。
func ensureConfigFiles() {
	if err := os.MkdirAll("config", 0755); err != nil {
		log.Printf("[!] 无法创建 config 目录: %v", err)
		return
	}
	required := []string{
		"config.yml",
		"auth.yml",
		"rate.yml",
		"editor.yml",
		"agents.yml",
		"ai.yml",
		"mcps.yml",
	}
	for _, name := range required {
		dst := filepath.Join("config", name)
		if _, err := os.Stat(dst); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			log.Printf("[!] 检查配置文件 %s 失败: %v", dst, err)
			continue
		}
		src := dst + ".example"
		data, err := os.ReadFile(src)
		if err != nil {
			log.Printf("[!] 缺少配置模板 %s，跳过自动生成 %s", src, dst)
			continue
		}
		if err := os.WriteFile(dst, data, 0644); err != nil {
			log.Printf("[!] 自动生成配置文件 %s 失败: %v", dst, err)
			continue
		}
		log.Printf("[Config] 首次启动：已从模板生成 %s（请按需修改密钥等配置）", dst)
	}
}

func loadStatusConfig() {
	data, err := os.ReadFile(filepath.Join("config", "status_config.json"))
	if err != nil {
		log.Printf("[!] 无法读取状态配置: %v", err)
		return
	}
	if err := json.Unmarshal(data, &statusConfig); err != nil {
		log.Printf("[!] 解析状态配置失败: %v", err)
		return
	}
	log.Printf("[Config] 状态配置: %d 个排序, %d 个归档 — %v", len(statusConfig.Order), len(statusConfig.Archived), statusConfig.Archived)
}

// editorFieldPresets 读取 config/editor.yml 的模组编辑器预填选项
type editorFieldPresets struct {
	LicenseOptions      []string `yaml:"license_options"`
	ArchitectureOptions []string `yaml:"architecture_options"`
	TagOptions          []string `yaml:"tag_options"`
}

func loadEditorFieldPresets() editorFieldPresets {
	var presets editorFieldPresets
	data, err := os.ReadFile(filepath.Join("config", "editor.yml"))
	if err != nil {
		return presets
	}
	if err := yaml.Unmarshal(data, &presets); err != nil {
		log.Printf("[!] 解析编辑器预填配置失败: %v", err)
	}
	return presets
}

// loadDifficultyCache 从 mods.db 的 mod_difficulty 表加载维护难度到内存缓存。
func loadDifficultyCache() {
	diffs, err := listModDifficultiesFromDB()
	if err != nil {
		log.Printf("[!] 无法从 mods.db 读取维护难度: %v", err)
		return
	}
	difficultyMu.Lock()
	difficultyMap = diffs
	difficultyMu.Unlock()
	log.Printf("[Rate] 已从 mods.db 加载 %d 个模组的维护难度", len(diffs))
}

// loadModMetas 从 mods.db 加载全部模组元数据到内存缓存。
func loadModMetas() {
	if err := loadModMetasFromDB(); err != nil {
		log.Printf("[!] 从 mods.db 加载模组元数据失败: %v", err)
	}
}
