// Package api 提供请求体JSON绑定和参数校验的错误处理功能。
// 将Gin框架的ShouldBindJSON和validator返回的英文错误信息
// 转换为对中文用户友好的提示信息，包括字段名转蛇形命名和类型提示。
package api

import (
	// encoding/json 提供JSON语法错误类型检测。
	"encoding/json"
	// errors 提供错误类型判断和比较功能。
	"errors"
	// fmt 提供格式化字符串输出。
	"fmt"
	// io 提供EOF错误常量。
	"io"
	// reflect 提供运行时类型反射，用于解析结构体的JSON标签和字段类型。
	"reflect"
	// strings 提供字符串分割和匹配功能。
	"strings"
	// unicode 提供Unicode字符分类（如大写字母判断）。
	"unicode"

	// github.com/gin-gonic/gin Gin Web框架，提供请求绑定功能。
	"github.com/gin-gonic/gin"
	// github.com/go-playground/validator/v10 结构体校验库，校验规则通过bind后触发。
	"github.com/go-playground/validator/v10"
)

// bindJSONOrFail 绑定请求体JSON到目标结构体，失败时自动返回友好的中文错误响应。
// 参数c：Gin请求上下文。
// 参数target：绑定的目标结构体指针。
// 参数fallback：无法确定具体错误原因时的兜底错误消息。
// 返回值：绑定成功返回true，失败返回false（已写入错误响应）。
func bindJSONOrFail(c *gin.Context, target any, fallback string) bool {
	// 尝试将请求体JSON绑定到目标结构体
	if err := c.ShouldBindJSON(target); err != nil {
		// 绑定失败，解析错误原因并返回中文错误消息
		Fail(c, 400, CodeBadRequest, bindJSONErrorMessage(err, target, fallback))
		return false
	}
	// 绑定成功
	return true
}

// bindJSONErrorMessage 将JSON绑定或校验错误转换为中文友好的错误消息。
// 按错误类型依次检查：JSON语法错误 → 空请求体 → 类型不匹配 → 校验失败 → 兜底。
// 参数err：ShouldBindJSON返回的原始错误。
// 参数target：绑定目标结构体（用于获取字段的JSON标签名）。
// 参数fallback：兜底错误消息。
// 返回值：中文错误消息字符串。
func bindJSONErrorMessage(err error, target any, fallback string) string {
	// 检测JSON语法错误（如缺少引号、多余逗号等）
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return "请求体必须是合法 JSON"
	}
	// 检测请求体为空
	if errors.Is(err, io.EOF) {
		return "请求体不能为空"
	}

	// 检测JSON值类型与实际字段类型不匹配（如字段期望数字却传入字符串）
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		// 获取目标结构体中对应的JSON字段名
		field := jsonFieldName(target, typeErr.Field)
		if field == "" {
			field = typeErr.Field
		}
		if field == "" {
			return fallback
		}
		// 返回"字段名 必须是期望类型"的中文消息
		return fmt.Sprintf("%s 必须是%s", field, jsonTypeName(typeErr.Type))
	}

	// 检测结构体校验错误（如required、min、max等validator规则）
	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) && len(validationErrs) > 0 {
		// 取第一个校验错误作为提示
		// 获取JSON字段名
		field := jsonFieldName(target, validationErrs[0].StructField())
		if field == "" {
			field = toSnakeCase(validationErrs[0].Field()) // 降级为蛇形命名
		}
		// required标签返回"为必填项"
		if validationErrs[0].Tag() == "required" {
			return fmt.Sprintf("%s 为必填项", field)
		}
		// 其他校验规则返回"不符合规范"
		return fmt.Sprintf("%s 不符合规范", field)
	}

	// 兜底：尝试从错误消息中提取Go字段名并映射为JSON字段名
	message := err.Error()
	for goName, jsonName := range jsonFieldMap(target) {
		// 在错误消息中查找Go字段名或JSON字段名
		if strings.Contains(message, "."+goName) || strings.Contains(message, " "+goName+" ") || strings.Contains(message, jsonName) {
			return fmt.Sprintf("%s 必须是%s", jsonName, jsonTypeName(fieldType(target, goName)))
		}
	}
	// 所有解析均失败，返回兜底消息
	return fallback
}

// jsonFieldName 将Go结构体字段路径名（可能包含"."如"User.Name"）映射为JSON标签名。
// 参数target：结构体实例或指针。
// 参数name：字段路径名（如"StructName.FieldName"）。
// 返回值：对应的JSON标签名；映射失败时返回蛇形命名字符串。
func jsonFieldName(target any, name string) string {
	// 空字段名直接返回空
	if name == "" {
		return ""
	}
	// 按"."分割路径，取最后一段作为字段名
	fields := strings.Split(name, ".")
	last := fields[len(fields)-1]
	// 从字段映射中查找JSON标签名
	if mapped, ok := jsonFieldMap(target)[last]; ok {
		return mapped
	}
	// 查找失败则转为蛇形命名
	return toSnakeCase(last)
}

// jsonFieldMap 构建目标结构体的Go字段名→JSON标签名映射表。
// 通过reflect读取结构体的json标签，构建双向映射（Go名→JSON名和JSON名→JSON名）。
// 参数target：结构体实例或指针。
// 返回值：Go字段名和JSON标签名到JSON标签名的映射。
func jsonFieldMap(target any) map[string]string {
	// 初始化结果map
	result := map[string]string{}
	// 获取目标类型的反射Type
	t := reflect.TypeOf(target)
	if t == nil {
		return result
	}
	// 如果是指针类型，获取指向的底层类型
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// 非结构体类型直接返回空映射
	if t.Kind() != reflect.Struct {
		return result
	}
	// 遍历结构体的所有字段
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		// 跳过未导出的字段（PkgPath不为空表示私有字段）
		if field.PkgPath != "" {
			continue
		}
		// 读取json标签的第一部分（如`json:"name,omitempty"`中的"name"）
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		// 无json标签时使用蛇形命名的Go字段名
		if name == "" {
			name = toSnakeCase(field.Name)
		}
		// json:"-"表示跳过该字段
		if name == "-" {
			continue
		}
		// 建立双向映射
		result[field.Name] = name // Go→JSON
		result[name] = name       // JSON→JSON（自映射）
	}
	return result
}

// fieldType 通过反射获取结构体指定Go字段名的类型。
// 参数target：结构体实例或指针。
// 参数goName：Go语言字段名。
// 返回值：字段的reflect.Type，未找到时返回nil。
func fieldType(target any, goName string) reflect.Type {
	// 获取目标类型的反射Type
	t := reflect.TypeOf(target)
	// 如果是指针，获取底层类型
	if t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// 非结构体或无类型信息返回nil
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	// 按Go字段名查找
	if field, ok := t.FieldByName(goName); ok {
		return field.Type
	}
	return nil
}

// jsonTypeName 将Go反射类型转换为中文类型名称，用于错误提示。
// 参数t：待转换的reflect.Type。
// 返回值：中文类型名称（如"数字"、"字符串"、" true 或 false"）。
func jsonTypeName(t reflect.Type) string {
	// 空类型
	if t == nil {
		return "有效值"
	}
	// 解引用指针以获取底层类型
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	// 按Kind映射为中文类型名
	switch t.Kind() {
	case reflect.Bool:
		return " true 或 false" // 布尔类型
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "数字" // 整数和浮点数
	case reflect.String:
		return "字符串" // 字符串类型
	default:
		return "有效值" // 其他类型
	}
}

// toSnakeCase 将驼峰命名的Go标识符转换为蛇形命名（snake_case）。
// 如"PageSize" → "page_size", "UserID" → "user_id"。
// 参数value：驼峰命名的字符串。
// 返回值：蛇形命名的字符串。
func toSnakeCase(value string) string {
	// 使用rune切片构建结果（正确处理Unicode字符）
	var out []rune
	for i, r := range value {
		// 遇到大写字母
		if unicode.IsUpper(r) {
			// 非首字母的大写前插入下划线
			if i > 0 {
				out = append(out, '_')
			}
			// 大写转小写
			out = append(out, unicode.ToLower(r))
			continue
		}
		// 非大写字母直接追加
		out = append(out, r)
	}
	return string(out)
}
