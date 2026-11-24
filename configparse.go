// configparse.go —— 项目配置文件的轻量结构化解析。
//
// 只做“把键值摊平”这一件事，不负责业务判断：
//   application.yml  → spring.datasource.url / username / password
//   .env             → DB_HOST / DB_PASSWORD
//   application.properties → spring.datasource.url
//
// 这样做是为了让接入向导不再靠“顶格 key + 正则”猜嵌套 YAML；
// 解析失败时调用方仍会回退到原来的文本扫描，不会因为一个新格式把旧项目弄坏。
package main

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

func parseYAMLFlat(text string) map[string]string {
	out := map[string]string{}
	dec := yaml.NewDecoder(strings.NewReader(text))
	for {
		var v any
		err := dec.Decode(&v)
		if err == io.EOF {
			break
		}
		if err != nil {
			// 半截文件也保留已解析到的键；完全解析失败时调用方会回退到正则。
			break
		}
		flattenYAML("", v, out)
	}
	return out
}

func flattenYAML(prefix string, v any, out map[string]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flattenYAML(key, val, out)
		}
	case map[any]any:
		for k, val := range x {
			name := fmt.Sprint(k)
			key := name
			if prefix != "" {
				key = prefix + "." + name
			}
			flattenYAML(key, val, out)
		}
	case []any:
		for i, val := range x {
			key := strconv.Itoa(i)
			if prefix != "" {
				key = prefix + "." + key
			}
			flattenYAML(key, val, out)
		}
	default:
		if prefix != "" {
			out[prefix] = scalarText(v)
		}
	}
}

func scalarText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

// flatGet 按“完整 key 优先、大小写不敏感”取值。
func flatGet(m map[string]string, keys ...string) string {
	for _, want := range keys {
		if v, ok := m[want]; ok {
			return v
		}
		for k, v := range m {
			if strings.EqualFold(k, want) {
				return v
			}
		}
	}
	return ""
}

// flatGetSuffix 用于 docker-compose / Spring 里带前缀的环境变量：
// services.db.environment.MYSQL_DATABASE 这类键，按最后一段取。
func flatGetSuffix(m map[string]string, names ...string) string {
	for _, want := range names {
		for k, v := range m {
			last := k
			if i := strings.LastIndexByte(k, '.'); i >= 0 {
				last = k[i+1:]
			}
			if strings.EqualFold(last, want) {
				return v
			}
		}
	}
	return ""
}

func lastKeyPart(k string) string {
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		return k[i+1:]
	}
	return k
}

func parseDotEnvFlat(text string) (map[string]string, error) {
	m, err := godotenv.Unmarshal(text)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// parsePropertiesFlat 只处理 Spring 常见的 key=value / key:value；
// 反斜杠续行也拼起来，避免长 JDBC URL 被截断。
func parsePropertiesFlat(text string) map[string]string {
	out := map[string]string{}
	var pending string
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if pending != "" {
			line = pending + strings.TrimLeft(line, " \t")
			pending = ""
		}
		if strings.HasSuffix(line, "\\") {
			pending = strings.TrimSuffix(line, "\\")
			continue
		}
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") || strings.HasPrefix(trim, "!") {
			continue
		}
		idx := strings.IndexAny(line, "=:")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key != "" {
			out[key] = val
		}
	}
	return out
}
