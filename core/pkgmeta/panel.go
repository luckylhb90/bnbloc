package pkgmeta

import "fmt"

// A plugin's own panel (docs/architecture/plugin-panels.md): a page of the
// component's own that the member opens from its card on their desktop, in
// the same sandboxed frame as a settings page. The page reaches its sidecar
// only through the desktop, which calls the sidecar's loopback API with the
// bearer it keeps (sidecar.tokenEnv), so the page never holds a credential.
// Nothing here names what a panel is for.

// validatePanel checks a package's panel: a page (validatePage), and the
// bearer the desktop calls the sidecar with.
func validatePanel(pkg Package) error {
	p := pkg.Panel
	if p == nil {
		return nil
	}
	if err := validatePage("panel", PanelBundle, p); err != nil {
		return err
	}
	if pkg.Sidecar == nil || pkg.Sidecar.TokenEnv == "" {
		return fmt.Errorf("a panel requires sidecar.tokenEnv: the desktop calls the sidecar for the page with that bearer")
	}
	return nil
}
