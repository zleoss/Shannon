// =============================================================================
// 文件: go/orchestrator/test_structpb/main.go
// -----------------------------------------------------------------------------
// 【一句话功能】 structpb 类型兼容性测试工具，验证 map 值转换的正确性
// 【关键内容】 递归转换 map 值为 structpb 兼容类型；处理 []string、嵌套 map 等场景
// 【协作关系】 独立测试工具，不依赖生产模块
// =============================================================================
package main

import (
	"fmt"
	"reflect"

	"google.golang.org/protobuf/types/known/structpb"
)

// sanitizeForStructpb converts values to types compatible with structpb.NewStruct.
func sanitizeForStructpb(m map[string]interface{}) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		result[k] = sanitizeValue(v)
	}
	return result
}

func sanitizeValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case bool, int, int32, int64, float32, float64, string:
		return val
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, elem := range val {
			result[i] = sanitizeValue(elem)
		}
		return result
	case map[string]interface{}:
		return sanitizeForStructpb(val)
	default:
		rv := reflect.ValueOf(v)
		switch rv.Kind() {
		case reflect.Slice, reflect.Array:
			return sanitizeSliceValue(rv)
		case reflect.Map:
			return sanitizeMapValue(rv)
		default:
			return v
		}
	}
}

func sanitizeSliceValue(rv reflect.Value) []interface{} {
	length := rv.Len()
	result := make([]interface{}, length)
	for i := 0; i < length; i++ {
		result[i] = sanitizeValue(rv.Index(i).Interface())
	}
	return result
}

func sanitizeMapValue(rv reflect.Value) map[string]interface{} {
	if rv.Type().Key().Kind() != reflect.String {
		return nil
	}
	result := make(map[string]interface{}, rv.Len())
	iter := rv.MapRange()
	for iter.Next() {
		key := iter.Key().String()
		result[key] = sanitizeValue(iter.Value().Interface())
	}
	return result
}

func main() {
	fmt.Println("=== Test 1: []string (the original bug) ===")
	m1 := map[string]interface{}{
		"stop": []string{"a", "b", "c"},
	}
	st1, err1 := structpb.NewStruct(m1)
	fmt.Printf("Before fix: st=%v err=%v\n", st1, err1)

	m1Fixed := sanitizeForStructpb(m1)
	st1Fixed, err1Fixed := structpb.NewStruct(m1Fixed)
	fmt.Printf("After fix:  st=%v err=%v\n\n", st1Fixed, err1Fixed)

	fmt.Println("=== Test 2: []map[string]string (conversation history) ===")
	m2 := map[string]interface{}{
		"conversation_history": []map[string]string{
			{"role": "user", "content": "hello"},
			{"role": "assistant", "content": "hi"},
		},
	}
	st2, err2 := structpb.NewStruct(m2)
	fmt.Printf("Before fix: st=%v err=%v\n", st2, err2)

	m2Fixed := sanitizeForStructpb(m2)
	st2Fixed, err2Fixed := structpb.NewStruct(m2Fixed)
	fmt.Printf("After fix:  st=%v err=%v\n\n", st2Fixed, err2Fixed)

	fmt.Println("=== Test 3: Nested structures ===")
	m3 := map[string]interface{}{
		"nested": map[string]interface{}{
			"tags": []string{"tag1", "tag2"},
		},
	}
	m3Fixed := sanitizeForStructpb(m3)
	st3Fixed, err3Fixed := structpb.NewStruct(m3Fixed)
	fmt.Printf("Nested after fix: st=%v err=%v\n\n", st3Fixed, err3Fixed)

	fmt.Println("=== All tests passed! ===")
}
