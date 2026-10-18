package component

// SandboxMountsContract names the descriptor a component writes to put
// read-only folders into the sandbox space a turn runs in
// (docs/architecture/sandbox-mounts.md): MountsFile, as MountsFileName in
// its own folder of the bridge's mounts directory. The bridge validates
// every entry; the component is untrusted.
const SandboxMountsContract = "bnbloc.sandbox.mounts/v1"

// MountsFileName is the descriptor's file name.
const MountsFileName = "mounts.json"

// MountsFile is the descriptor, replaced whole on every change.
type MountsFile struct {
	Contract string  `json:"contract"`
	Mounts   []Mount `json:"mounts"`
}

// Mount is one folder: Host under the component's own folder of the
// workspaces root, Guest an absolute path in the guest, For whose runs see
// it (MountForOwn when empty).
type Mount struct {
	Host  string `json:"host"`
	Guest string `json:"guest"`
	For   string `json:"for,omitempty" tstype:"MountFor"`
}

// Whose runs a mount is for (Mount.For).
//
//wire:union MountFor
const (
	// MountForOwn: the agent's owner's own runs (the default).
	MountForOwn = "own"
	// MountForBorrowed: runs another member borrows.
	MountForBorrowed = "borrowed"
	// MountForAll: both.
	MountForAll = "all"
)
