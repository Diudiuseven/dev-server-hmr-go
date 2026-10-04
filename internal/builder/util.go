package builder

import (
	"os"
	"time"
)

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }
func nowTime() time.Time                   { return time.Now() }
