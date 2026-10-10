package pkgmeta

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	sidecarFileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	sidecarEnvNameRE  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// SidecarFileArtifactKey is the Package.Artifacts key of a declared sidecar
// file for one platform: "<platform>/<name>".
func SidecarFileArtifactKey(platform, name string) string { return platform + "/" + name }

// validateSidecarShape is the kind-neutral rule set of the shared sidecar
// launch block (any kind with a sidecar): identifier-shaped, unique env names
// across listenEnv/supervisedEnv/tokenEnv/dataDirEnv/files, well-formed
// unique file names, and an artifact map where every "<platform>/<name>" key
// is a declared file of a platform that has a main artifact, and every
// platform with a main artifact carries every declared file.
func validateSidecarShape(pkg Package) error {
	var files []SidecarFile
	if sc := pkg.Sidecar; sc != nil {
		envs := map[string]string{}
		claim := func(field, env string) error {
			if env == "" {
				return nil
			}
			if !sidecarEnvNameRE.MatchString(env) {
				return fmt.Errorf("sidecar.%s %q must be an upper-case identifier", field, env)
			}
			if prev, dup := envs[env]; dup {
				return fmt.Errorf("sidecar.%s %q collides with sidecar.%s", field, env, prev)
			}
			envs[env] = field
			return nil
		}
		for _, f := range []struct{ field, env string }{
			{"listenEnv", sc.ListenEnv}, {"supervisedEnv", sc.SupervisedEnv},
			{"tokenEnv", sc.TokenEnv}, {"dataDirEnv", sc.DataDirEnv},
		} {
			if err := claim(f.field, f.env); err != nil {
				return err
			}
		}
		names := map[string]bool{}
		for i, file := range sc.Files {
			if !sidecarFileNameRE.MatchString(file.Name) {
				return fmt.Errorf("sidecar.files[%d].name %q must use lowercase letters, digits, dot, underscore, or dash", i, file.Name)
			}
			if names[file.Name] {
				return fmt.Errorf("sidecar.files: duplicate name %q", file.Name)
			}
			names[file.Name] = true
			if strings.TrimSpace(file.Env) == "" {
				return fmt.Errorf("sidecar.files[%d] %q requires env", i, file.Name)
			}
			if err := claim(fmt.Sprintf("files[%d].env", i), file.Env); err != nil {
				return err
			}
		}
		files = sc.Files
	}
	return validateSidecarFileArtifacts(pkg.Artifacts, files)
}

func validateSidecarFileArtifacts(artifacts map[string]Artifact, files []SidecarFile) error {
	declared := map[string]bool{}
	for _, f := range files {
		declared[f.Name] = true
	}
	platforms := []string{}
	for key := range artifacts {
		platform, name, extra := strings.Cut(key, "/")
		if !extra {
			platforms = append(platforms, key)
			continue
		}
		if !declared[name] {
			return fmt.Errorf("artifacts.%s: %q is not a declared sidecar file", key, name)
		}
		if _, ok := artifacts[platform]; !ok {
			return fmt.Errorf("artifacts.%s: platform %q has no main artifact", key, platform)
		}
	}
	sort.Strings(platforms)
	for _, platform := range platforms {
		for _, f := range files {
			if _, ok := artifacts[SidecarFileArtifactKey(platform, f.Name)]; !ok {
				return fmt.Errorf("artifacts.%s requires sidecar file %q", SidecarFileArtifactKey(platform, f.Name), f.Name)
			}
		}
	}
	return nil
}
