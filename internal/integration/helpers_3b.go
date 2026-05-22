package integration

import "os"

// osRemoveAll is a thin shim around os.RemoveAll, kept in its own file
// so the test files don't need to import "os" individually.
func osRemoveAll(path string) error { return os.RemoveAll(path) }
