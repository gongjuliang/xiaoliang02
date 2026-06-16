// Package api 提供请求体JSON绑定和参数校验的错误处理。
// 当Gin的ShouldBindJSON返回验证错误时，自动将字段级校验错误
// 转换为标准API错误响应，使用snake_case格式匹配前端期望的字段名。
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

func bindJSONOrFail(c *gin.Context, target any, fallback string) bool {
	if err := c.ShouldBindJSON(target); err != nil {
		Fail(c, 400, CodeBadRequest, bindJSONErrorMessage(err, target, fallback))
		return false
	}
	return true
}

func bindJSONErrorMessage(err error, target any, fallback string) string {
	var syntaxErr *json.SyntaxError
	if errors.As(err, &syntaxErr) {
		return "请求体必须是合法 JSON"
	}
	if errors.Is(err, io.EOF) {
		return "请求体不能为空"
	}

	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := jsonFieldName(target, typeErr.Field)
		if field == "" {
			field = typeErr.Field
		}
		if field == "" {
			return fallback
		}
		return fmt.Sprintf("%s 必须是%s", field, jsonTypeName(typeErr.Type))
	}

	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) && len(validationErrs) > 0 {
		field := jsonFieldName(target, validationErrs[0].StructField())
		if field == "" {
			field = toSnakeCase(validationErrs[0].Field())
		}
		if validationErrs[0].Tag() == "required" {
			return fmt.Sprintf("%s 为必填项", field)
		}
		return fmt.Sprintf("%s 不符合规范", field)
	}

	message := err.Error()
	for goName, jsonName := range jsonFieldMap(target) {
		if strings.Contains(message, "."+goName) || strings.Contains(message, " "+goName+" ") || strings.Contains(message, jsonName) {
			return fmt.Sprintf("%s 必须是%s", jsonName, jsonTypeName(fieldType(target, goName)))
		}
	}
	return fallback
}

func jsonFieldName(target any, name string) string {
	if name == "" {
		return ""
	}
	fields := strings.Split(name, ".")
	last := fields[len(fields)-1]
	if mapped, ok := jsonFieldMap(target)[last]; ok {
		return mapped
	}
	return toSnakeCase(last)
}

func jsonFieldMap(target any) map[string]string {
	result := map[string]string{}
	t := reflect.TypeOf(target)
	if t == nil {
		return result
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return result
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if field.PkgPath != "" {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" {
			name = toSnakeCase(field.Name)
		}
		if name == "-" {
			continue
		}
		result[field.Name] = name
		result[name] = name
	}
	return result
}

func fieldType(target any, goName string) reflect.Type {
	t := reflect.TypeOf(target)
	if t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	if field, ok := t.FieldByName(goName); ok {
		return field.Type
	}
	return nil
}

func jsonTypeName(t reflect.Type) string {
	if t == nil {
		return "有效值"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return " true 或 false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "数字"
	case reflect.String:
		return "字符串"
	default:
		return "有效值"
	}
}

func toSnakeCase(value string) string {
	var out []rune
	for i, r := range value {
		if unicode.IsUpper(r) {
			if i > 0 {
				out = append(out, '_')
			}
			out = append(out, unicode.ToLower(r))
			continue
		}
		out = append(out, r)
	}
	return string(out)
}
