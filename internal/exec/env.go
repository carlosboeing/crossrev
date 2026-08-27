package exec

import "os"

// Environ returns a copy of strings representing the environment.
func Environ() []string {
	return os.Environ()
}
