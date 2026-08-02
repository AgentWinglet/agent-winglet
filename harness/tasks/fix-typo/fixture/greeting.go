package fixture

import "fmt"

// Greet returns a greeting for name.
func Greet(name string) string {
	return fmt.Sprintf("helo, %s", name)
}
