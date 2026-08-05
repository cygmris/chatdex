package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/cygmris/chatdex/internal/llm"
)

// FieldError 指出是**哪一项**不合法。
//
// 「保存失败」这四个字对使用者没用——他要知道回去改哪一格。
type FieldError struct {
	Key string `json:"key"`
	Msg string `json:"msg"`
}

func (e FieldError) Error() string { return e.Key + "：" + e.Msg }

// Validate 逐项校验。返回全部错误而不是第一个——一次改完好过来回试。
func Validate(c Config) []FieldError {
	var errs []FieldError
	add := func(k, m string) { errs = append(errs, FieldError{Key: k, Msg: m}) }

	// 这条不因为「界面上能填」就放宽（需求 4.5）
	if err := llm.ValidateEndpoint(c.LLM.Endpoint); err != nil {
		add("llm.endpoint", err.Error())
	}

	for _, f := range Fields() {
		v := c.Get(f.Key)
		switch f.Kind {
		case "int", "bytes":
			n := toInt64(v)
			// Min 未显式声明时按 0 处理，而不是「不校验」——
			// 本项目的数值配置没有一个允许负数，之前写成 f.Min != 0 时
			// throttle_ms = -1 会被放行
			if n < f.Min {
				add(f.Key, fmt.Sprintf("不能小于 %d", f.Min))
			}
			if f.Max != 0 && n > f.Max {
				add(f.Key, fmt.Sprintf("不能大于 %d", f.Max))
			}
		case "enum":
			// 模型类的 Options 是运行时填的，这里只校验固定枚举
			if len(f.Options) > 0 {
				s, _ := v.(string)
				if !contains(f.Options, s) {
					add(f.Key, fmt.Sprintf("只能是 %s 之一", strings.Join(f.Options, " / ")))
				}
			}
		case "string":
			if s, _ := v.(string); !f.Optional && strings.TrimSpace(s) == "" {
				add(f.Key, "不能为空")
			}
		}
	}
	if c.Ports.UI == c.Ports.API {
		add("ports.api", "不能与 dashboard 端口相同")
	}
	return errs
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	}
	return 0
}

// Save 把配置写到 path。
//
// 两个刻意的选择：
//
//  1. **只写与默认值不同的键**。这样文件里永远只有「你改过的东西」，
//     将来默认值调整能自动跟随，而不是被一份固化的旧默认值锁死。
//  2. **原子写**：临时文件 → chmod 0600 → rename。断电不会留下半个配置文件，
//     而半个 JSON 会让服务下次启动直接失败。
func Save(path string, c Config) error {
	if errs := Validate(c); len(errs) > 0 {
		return errs[0]
	}
	diff := diffFromDefault(c)

	buf, err := json.MarshalIndent(diff, "", "  ")
	if err != nil {
		return err
	}
	buf = append(buf, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("建配置目录: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o600); err != nil {
		return fmt.Errorf("写临时配置: %w", err)
	}
	// WriteFile 的权限受 umask 影响，显式收一次
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("替换配置文件: %w", err)
	}
	return nil
}

// diffFromDefault 只保留与默认值不同的键，输出嵌套的 map 以便 JSON 结构与
// 手写配置一致（section → key）。
func diffFromDefault(c Config) map[string]any {
	def := Default()
	out := map[string]any{}

	put := func(key string, v any) {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) == 1 {
			out[key] = v
			return
		}
		sec, ok := out[parts[0]].(map[string]any)
		if !ok {
			sec = map[string]any{}
			out[parts[0]] = sec
		}
		sec[parts[1]] = v
	}

	for _, f := range Fields() {
		cur, base := c.Get(f.Key), def.Get(f.Key)
		// 必须用 DeepEqual 而不是 !=：Get 返回 any，而 `!=` 在接口装着
		// **不可比较类型**（切片、map）时会直接 panic —— 不是返回 false，是崩。
		// 在 backup.sources（[]BackupSource）加进来之前，所有配置值恰好都是
		// 标量，于是这个假设一直没被戳破。
		if !reflect.DeepEqual(cur, base) {
			put(f.Key, cur)
		}
	}
	return out
}
