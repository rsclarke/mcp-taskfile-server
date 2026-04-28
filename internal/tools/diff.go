package tools

import "bytes"

// Equal reports whether two registered tools are equivalent by
// comparing Name, Description, and the cached InputSchema bytes captured
// when the tool was created.
func Equal(a, b *RegisteredTool) bool {
	return a.Name == b.Name &&
		a.Description == b.Description &&
		bytes.Equal(a.schemaBytes, b.schemaBytes)
}

// Diff compares the old registered tool set against the desired set and
// returns the names of tools to remove and tools to add (or re-add due
// to changes).
func Diff(old, desired map[string]RegisteredTool) (stale, added []string) {
	for name, oldTool := range old {
		if newTool, ok := desired[name]; !ok {
			stale = append(stale, name)
		} else if !Equal(&oldTool, &newTool) {
			stale = append(stale, name)
		}
	}
	for name, newTool := range desired {
		if oldTool, ok := old[name]; ok && Equal(&oldTool, &newTool) {
			continue
		}
		added = append(added, name)
	}
	return stale, added
}
