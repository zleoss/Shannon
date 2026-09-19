// =============================================================================
// 文件: go/orchestrator/internal/templates/loader.go
// -----------------------------------------------------------------------------
// 【一句话功能】
//   模板单文件加载：从文件/io.Reader 解析 YAML 为 Template 对象。
// 【关键内容】
//   LoadTemplateFromFile :12 / LoadTemplate :26 / decodeTemplate :34
// 【协作关系】
//   被 templates.Registry.loadFile 调用产出 Entry，再交由 compiler 编译。
// =============================================================================

package templates

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadTemplateFromFile reads a YAML template from disk.
func LoadTemplateFromFile(path string) (*Template, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open template %s: %w", path, err)
	}
	defer f.Close()
	tpl, err := decodeTemplate(f)
	if err != nil {
		return nil, fmt.Errorf("decode template %s: %w", path, err)
	}
	return tpl, nil
}

// LoadTemplate parses a template from the provided reader.
func LoadTemplate(r io.Reader) (*Template, error) {
	tpl, err := decodeTemplate(r)
	if err != nil {
		return nil, fmt.Errorf("decode template: %w", err)
	}
	return tpl, nil
}

func decodeTemplate(r io.Reader) (*Template, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var tpl Template
	if err := dec.Decode(&tpl); err != nil {
		return nil, err
	}
	return &tpl, nil
}
