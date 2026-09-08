package server

import (
	"errors"
	"os"
	"path/filepath"
)

// Validate only the directory explicitly selected by the operator. Never
// enumerate home, siblings, protected directories or directory contents.
func validateSelectedDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("选择器未返回绝对目录路径")
	}
	info, err := os.Stat(path)
	if err != nil {
		return errors.New("所选目录不可访问，请检查路径及系统权限")
	}
	if !info.IsDir() {
		return errors.New("请选择项目文件夹，而不是文件")
	}
	return nil
}
