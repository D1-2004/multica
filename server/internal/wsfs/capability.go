package wsfs

import "encoding/json"

// SharedDiskCapability is the runtime metadata flag for shared-disk launch.
// An image name is not a capability: missing or unknown metadata skips the
// shared disk and keeps the historical private launch.
const SharedDiskCapability = "workspace_shared_disk"

// DeclaresSharedDisk reports whether runtime metadata lists the shared-disk
// capability. Invalid or absent metadata is not a declaration.
func DeclaresSharedDisk(metadata []byte) bool {
	if len(metadata) == 0 {
		return false
	}
	var doc struct {
		Capabilities []string `json:"capabilities"`
	}
	if err := json.Unmarshal(metadata, &doc); err != nil {
		return false
	}
	for _, capability := range doc.Capabilities {
		if capability == SharedDiskCapability {
			return true
		}
	}
	return false
}
