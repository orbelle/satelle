package provider

import "os"

func writeScript(path, body string) error { return os.WriteFile(path, []byte(body), 0o755) }

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
