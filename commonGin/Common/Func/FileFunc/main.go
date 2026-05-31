package FileFunc

import "os"

// CheckAllPathIsExist 批量检查多个文件路径是否全部存在
//
// 参数:
//   - paths: 需要检查的文件路径列表
//
// 返回值:
//   - bool: 所有路径都存在时返回 true，否则返回 false
//   - []string: 不存在的路径列表，全部存在时返回 nil
func CheckAllPathIsExist(paths []string) (bool, []string) {
	var notExistPaths []string
	// 遍历路径列表，收集不存在的路径
	for _, path := range paths {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			notExistPaths = append(notExistPaths, path)
		}
	}
	// 全部存在时返回 true
	if len(notExistPaths) == 0 {
		return true, nil
	}
	return false, notExistPaths
}

// CheckPathType 检查指定路径的存在性及其类型（文件还是目录）
//
// 参数:
//   - path: 需要检查的文件或目录路径
//
// 返回值:
//   - exists: 路径是否存在
//   - isDir: 路径是否为目录
//   - err: 除路径不存在外的其他错误，路径不存在时返回 nil
func CheckPathType(path string) (exists bool, isDir bool, err error) {
	// 获取路径信息
	info, err := os.Stat(path)
	// 处理错误情况：区分路径不存在与其他错误
	if err != nil {
		if os.IsNotExist(err) {
			return false, false, nil // Path doesn't exist
		}
		return false, false, err // Other error occurred
	}

	return true, info.IsDir(), nil
}
