//go:build !windows

package imagen

import "os"

func writePrivateTestFile(path string, data []byte) error { return os.WriteFile(path, data, 0600) }
func makeTestFilePublic(path string) error                { return os.Chmod(path, 0644) }
